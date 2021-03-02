package dataobjects

import (
	"bufio"
	"context"
	"fmt"
	"regexp"
	"strings"

	global "../Global"
	SQL "../Sql/Proxy"

	//"github.com/go-sql-driver/mysql"
	"os"
	"strconv"
	"time"

	log "github.com/sirupsen/logrus"
)

type (
	//Locker is the Object handling the Lock at cluster level and the local process for the node demanded to run the scheduler
	//Locker initialize also the ProxySQL node that will execute the actions

	Locker struct {
		MyServerIp             string
		MyServerPort           int
		MyServer               *ProxySQLNode
		myConfig               *global.Configuration
		FileLock               string
		FileLockPath           string
		FileLockInterval       int64
		FileLockReset          bool
		ClusterLockId          string
		ClusterLockInterval    int64
		ClusterLockReset       bool
		ClusterLastLockTime    int64
		ClusterCurrentLockTime int64
		IsClusterLocked        bool
		IsFileLocked           bool
		isLooped               bool
		LockFileTimeout        int64
		LockClusterTimeout     int64
	}
)

//Initialize the locker
//TODO initialize
func (locker *Locker) Init(config *global.Configuration) bool {
	locker.myConfig = config
	locker.MyServerIp = config.ProxySQL.Host
	locker.MyServerPort = config.ProxySQL.Port
	var MyServer = new(ProxySQLNode)
	locker.MyServer = MyServer
	locker.MyServer.Ip = locker.MyServerIp
	locker.FileLockPath = config.ProxySQL.LockFilePath
	locker.isLooped = config.Global.Daemonize
	locker.LockFileTimeout = config.Global.LockFileTimeout
	locker.LockClusterTimeout = config.Global.LockClusterTimeout

	// Set lock file name based on the PXC cluster ID + HGs
	locker.ClusterLockId = strconv.Itoa(config.PxcCluster.ClusterID) +
		"_HG_" + strconv.Itoa(config.PxcCluster.HgW) +
		"_W_HG_" + strconv.Itoa(config.PxcCluster.HgR) +
		"_R"
	locker.FileLock = locker.ClusterLockId

	log.Info("Locker initialized")
	return true
}

//TODO fill the method
func (locker *Locker) CheckFileLock() *ProxySQLNode {

	log.Info("")

	return locker.MyServer
}

// we will check if the node were we are has a lock or if can acquire one.
// If not we will return nil to indicate program must exit given either there is already another node holding the lock
// or this node is not in a good state to acquire a lock
// Outside call To get and check the active list of ProxySQL server we call ProxySQLCLuster.GetProxySQLnodes
// All the DB operations are done connecting locally to the ProxySQL node running the scheduler

func (locker *Locker) CheckClusterLock() *ProxySQLNode {
	//TODO
	// 1 get connection
	// 2 get all we need from ProxySQL
	// 3 put the lock if we can
	global.SetPerformanceObj("Cluster lock", true, log.InfoLevel)
	proxySQLCluster := new(ProxySQLCluster)
	if !locker.MyServer.IsInitialized {
		if !locker.MyServer.Init(locker.myConfig) {
			global.SetPerformanceObj("Cluster lock", false, log.InfoLevel)
			return nil
		}
	}
	if locker.MyServer.IsInitialized {
		proxySQLCluster.User = locker.myConfig.ProxySQL.User
		proxySQLCluster.Password = locker.myConfig.ProxySQL.Password
		//myMap := new(map[string]ProxySQLNode)
		//log.Info(myMap)
		proxySQLCluster.Nodes = make(map[string]ProxySQLNode)
		if proxySQLCluster.GetProxySQLnodes(locker.MyServer) && len(proxySQLCluster.Nodes) > 0 {
			if nodes, ok := locker.findLock(proxySQLCluster.Nodes); ok && nodes != nil {
				if locker.PushSchedulerLock(nodes) {
					global.SetPerformanceObj("Cluster lock", false, log.InfoLevel)
					return locker.MyServer
				} else {
					global.SetPerformanceObj("Cluster lock", false, log.InfoLevel)
					return nil
				}
			} else {
				log.Info(fmt.Sprintf("Cannot put a lock on the cluster for this scheduler %s another node hold the lock and acting", locker.MyServer.Dns))
				global.SetPerformanceObj("Cluster lock", false, log.InfoLevel)
				return nil
			}
		}
	}

	global.SetPerformanceObj("Cluster lock", false, log.InfoLevel)
	return locker.MyServer
}

/*
Find lock method review all the nodes existing in the ProxySQL for an active LOck
it checks only nodes that are reachable.
Checks for:
	- existing lock locally
	- lock on another node
	- lock time comparing it with lockclustertimeout parameter
*/
func (locker *Locker) findLock(nodes map[string]ProxySQLNode) (map[string]ProxySQLNode, bool) {
	lockText := ""
	winningNode := ""
	lockHeader := "#LOCK_" + locker.ClusterLockId + "_"
	lockTail := "_LOCK#"
	lockHeaderLen := len(lockHeader)
	log.Debug("Using lock ID: ", lockHeader)
	//Capture the current time
	locker.ClusterCurrentLockTime = time.Now().UnixNano()
	log.Debug("Locker time: ", locker.ClusterCurrentLockTime)

	//let us process the nodes to identify if we have a lock, where, and if is expired
	for _, node := range nodes {
		lockText = node.Comment

		// the node we are parsing hold a LOCK
		if strings.Contains(lockText, lockHeader) {
			node.HoldLock = true

			startIdx := strings.Index(node.Comment, lockHeader)
			endIdx := strings.Index(node.Comment, lockTail)
			lockText := node.Comment[startIdx+lockHeaderLen : endIdx]

			log.Debug(fmt.Sprintf("Cluster Node %s has a scheduler lock ", node.Dns))

			//get LOCK time and assign as winning node the node that has be the more recent one
			node.LastLockTime = int64(global.ToInt(lockText))

			//Also if the time is not expired I will remove the lock text from comment for my node, given if the lock on another node is active I will not do a thing.
			//But if is not I will have the comment in the node ready
			//trim some double spaces to be sure we have a clean string
			node.Comment = node.Comment[:startIdx] + node.Comment[endIdx+6:]
			space := regexp.MustCompile(`\s+`)
			node.Comment = space.ReplaceAllString(node.Comment, " ")

			//check if we had exceed the lock time
			//convert nanoseconds to seconds
			lockTime := (locker.ClusterCurrentLockTime - node.LastLockTime) / 1000000000
			if lockTime > locker.LockClusterTimeout {
				log.Debug(fmt.Sprintf("The lock on node %s has expired from %d seconds", node.Dns, lockTime))
				node.IsLockExpired = true
			}

			// in case of multiple locks, the node with the most recent lock time wins
			if node.LastLockTime < locker.ClusterLastLockTime && !node.IsLockExpired {
				locker.ClusterLastLockTime = node.LastLockTime
				winningNode = node.Dns
			} else if locker.ClusterLastLockTime == 0 && !node.IsLockExpired {
				locker.ClusterLastLockTime = node.LastLockTime
				winningNode = node.Dns
			}
		}
		nodes[node.Dns] = node
	}

	// My ProxySQL node is the winner and I can push a lock on it
	// Else I exit without doing a bit
	if winningNode == "" || winningNode == locker.MyServer.Dns {
		node := nodes[locker.MyServer.Dns]
		if node.Dns != "" {
			node.Comment = node.Comment + " " + lockHeader + strconv.FormatInt(locker.ClusterCurrentLockTime, 10) + lockTail
			nodes[locker.MyServer.Dns] = node
			log.Debug(fmt.Sprintf("Lock acquired by node %s Current time %d", locker.MyServer.Dns, locker.ClusterCurrentLockTime))
		}

		return nodes, true
	}
	return nil, false

}

// We are ready to submit our changes. As always all is executed in a transaction
// TODO SHOULD we remove the proxysql node that doesn't work ????
func (locker *Locker) PushSchedulerLock(nodes map[string]ProxySQLNode) bool {
	if len(nodes) <= 0 {
		return true
	}

	if global.Performance {
		global.SetPerformanceObj("Execute SQL changes - ProxySQL cluster LOCK ("+locker.ClusterLockId+")", true, log.DebugLevel)
	}
	//We will execute all the commands inside a transaction if any error we will roll back all
	ctx := context.Background()
	tx, err := locker.MyServer.Connection.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal("Error in creating transaction to push changes ", err)
	}
	for key, node := range nodes {
		if node.Dns != "" {
			sqlAction := strings.ReplaceAll(SQL.Dml_update_comment_proxy_servers, "?", node.Comment) + " where hostname='" + node.Ip + "' and port= " + strconv.Itoa(node.Port)
			_, err = tx.ExecContext(ctx, sqlAction)
			if err != nil {
				tx.Rollback()
				log.Fatal("Error executing SQL: ", sqlAction, " for node: ", key, " Rollback and exit")
				log.Error(err)
				return false
			}
		}
	}
	err = tx.Commit()
	if err != nil {
		log.Fatal("Error IN COMMIT exit")
		return false

	} else {
		_, err = locker.MyServer.Connection.Exec("LOAD proxysql servers to RUN ")
		if err != nil {
			log.Fatal("Cannot load new proxysql configuration to RUN ")
			return false
		} else {
			_, err = locker.MyServer.Connection.Exec("save proxysql servers to disk ")
			if err != nil {
				log.Fatal("Cannot save new proxysql configuration to DISK ")
				return false
			}
		}

	}
	if global.Performance {
		global.SetPerformanceObj("Execute SQL changes - ProxySQL cluster LOCK ("+locker.ClusterLockId+")", false, log.DebugLevel)
	}

	return true
}

func (locker *Locker) SetLockFile() bool {
	if locker.FileLock == "" {
		log.Error("Lock Filename is invalid (empty) ")
		return false
	}
	fullFile := locker.FileLockPath + string(os.PathSeparator) + locker.FileLock
	if _, err := os.Stat(fullFile); err == nil && !locker.isLooped {
		fmt.Printf("A lock file named: %s  already exists.\n If this is a refuse of a dirty execution remove it manually to have the check able to run\n", fullFile)
		return false
	} else {
		sampledata := []string{"PID:" + strconv.Itoa(os.Getpid()),
			"Time:" + strconv.FormatInt(time.Now().Unix(), 10),
		}

		file, err := os.OpenFile(fullFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

		if err != nil {
			log.Error(fmt.Sprintf("failed creating lock file: %s", err.Error()))
			return false
		}

		datawriter := bufio.NewWriter(file)

		for _, data := range sampledata {
			_, _ = datawriter.WriteString(data + "\n")
		}

		datawriter.Flush()
		file.Close()
	}

	return true
}

func (locker *Locker) RemoveLockFile() bool {
	e := os.Remove(locker.FileLockPath + string(os.PathSeparator) + locker.FileLock)
	if e != nil {
		log.Fatalf("Cannot remove lock file %s", e)
	}
	return true
}
