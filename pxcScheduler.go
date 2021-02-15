package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"time"

	DO "./lib/DataObjects"
	Global "./lib/Global"
	_ "github.com/go-sql-driver/mysql"
	log "github.com/sirupsen/logrus"
)

/*
#######################################
#
# ProxySQL galera check v2
#
# Author Marco Tusa
# Copyright (C) (2016 - 2021)
#
#
#THIS PROGRAM IS PROVIDED "AS IS" AND WITHOUT ANY EXPRESS OR IMPLIED
#WARRANTIES, INCLUDING, WITHOUT LIMITATION, THE IMPLIED WARRANTIES OF
#MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE.
#
#This program is free software; you can redistribute it and/or modify it under
#the terms of the GNU General Public License as published by the Free Software
#Foundation, version 3;
#
#You should have received a copy of the GNU General Public License along with
#this program; if not, write to the Free Software Foundation, Inc., 59 Temple
#Place, Suite 330, Boston, MA  02111-1307  USA.

#######################################
*/
/*
Main function must contains only initial parameter, log system init and main object init
*/
func main() {
	//global setup of basic parameters
	const (
		Separator = string(os.PathSeparator)
	)
	var devLoopWait = 0
	var devLoop = 0
	var lockId string //LockId is compose by clusterID_HG_W_HG_R

	// By default performance collection is disabled
	Global.Performance = false
	//initialize help
	help := new(Global.HelpText)
	help.Init()

	config, err := Global.GetConfig(os.Args, Global.GetConfigFromFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "\n%s\n", help.GetHelpText())
		os.Exit(1)
	}

	// Set lock file
	lockId = strconv.Itoa(config.Pxcluster.ClusterId) +
		"_HG_" + strconv.Itoa(config.Pxcluster.HgW) +
		"_W_HG_" + strconv.Itoa(config.Pxcluster.HgR) +
		"_R"

	if !config.Global.Development {
		if !setLockFile(lockId) {
			fmt.Print("Cannot create a lock, exit")
			os.Exit(1)
		}
	} else {
		devLoop = 2
		devLoopWait = config.Global.DevInterval
	}

	for i := 0; i <= devLoop; {

		//initialize the log system
		Global.InitLog(*config)
		//should we track performance or not
		Global.Performance = config.Global.Performance

		/*
			main game start here defining the Proxy Objects
		*/
		//initialize performance collection if requested
		if Global.Performance {
			Global.PerformanceMapOrdered = Global.NewOrderedMap()
			Global.PerformanceMap = Global.NewRegularIntMap()
			Global.SetPerformanceObj("main", true, log.ErrorLevel)
		}
		// create the two main containers the ProxySQL cluster and at least ONE ProxySQL node
		proxysqlCluster := new(DO.ProxySQLCluster)
		proxysqlNode := new(DO.ProxySQLNode)

		/*
			TODO the check against a cluster require a PRE-phase to align the nodes and an AFTER to be sure nodes settings are distributed.
			Not yet implemented
		*/
		if config.Proxysql.Clustered {
			proxysqlCluster.Active = true
			proxysqlCluster.User = config.Proxysql.User
			proxysqlCluster.Password = config.Proxysql.Password
			lockTime := time.Now().UnixNano()
			log.Debug("Lock time ", lockTime)

			nodes := proxysqlCluster.GetProxySQLnodes()
			log.Info(" Number of ProxySQL cluster nodes: ", len(nodes))

			// TODO just add the definition of which node we will use (always prefer local) to the proxysql node to continue

		}
		{
			//ProxySQL Node work start here
			if proxysqlNode.Init(*config) {
				if log.GetLevel() == log.DebugLevel {
					log.Debug("ProxySQL node initialized ")
				}
			} else {
				log.Error("Initialization failed")
				os.Exit(1)
			}
		}

		/*
			If we are here all the init phase is over and nodes should be containing the current status.
			Is it now time to evaluate what needs to be done.
			Priority is given to ANY service interruption as if Writer does not exists
			We will have 3 moments:
				- identify any anomalies (evaluateAllNodes)
				- check for the writers and failover / failback (evaluateWriters)
					- Fail-over will take action immediately, evaluating which is the best candidate.
				- check for readers and WriterIsAlso reader option
		*/

		/*
			Analyse the nodes and identify the list of nodes that we must take action on
			The action Map contains all the nodes that require modification with the proper Action ID set
		*/
		proxysqlNode.ActionNodeList = proxysqlNode.MySQLCluster.GetActionList()

		// Once we have the Map we translate it into SQL commands to process
		proxysqlNode.ProcessChanges()

		/*
			Final cleanup
		*/
		if proxysqlNode != nil {
			if proxysqlNode.CloseConnection() {
				if log.GetLevel() == log.DebugLevel {
					log.Info("Connection close")
				}
			}
		}

		if Global.Performance {
			Global.SetPerformanceObj("main", false, log.ErrorLevel)
			Global.PerformanceMapOrdered.Get("pippo")
			Global.ReportPerformance()
		}

		if config.Global.Development {
			time.Sleep(time.Duration(devLoopWait) * time.Millisecond)
		} else {
			i++
		}
		log.Info("")
	}
	if !config.Global.Development {
		removeLockFile(lockId)
	}

}

func setLockFile(lockId string) bool {

	if _, err := os.Stat("/tmp/" + lockId); err == nil {
		fmt.Printf("A lock file named: /tmp/%s  already exists.\n If this is a refuse of a dirty execution remove it manually to have the check able to run\n", lockId)
		return false
	} else {
		sampledata := []string{"PID:" + strconv.Itoa(os.Getpid()),
			"Time:" + strconv.FormatInt(time.Now().Unix(), 10),
		}

		file, err := os.OpenFile("/tmp/"+lockId, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)

		if err != nil {
			fmt.Printf("failed creating lock file: %s", err)
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

func removeLockFile(lockId string) bool {
	e := os.Remove("/tmp/" + lockId)
	if e != nil {
		log.Fatalf("Cannot remove lock file %s", e)
	}
	return true
}
