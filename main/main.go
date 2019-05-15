// Copyright (c) 2018 Couchbase, Inc.
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
// except in compliance with the License. You may obtain a copy of the License at
//   http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under the
// License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing permissions
// and limitations under the License.

package main

import (
	"flag"
	"fmt"
	xdcrBase "github.com/couchbase/goxdcr/base"
	xdcrLog "github.com/couchbase/goxdcr/log"
	"github.com/couchbase/goxdcr/metadata"
	"github.com/couchbase/goxdcr/metadata_svc"
	xdcrParts "github.com/couchbase/goxdcr/parts"
	"github.com/couchbase/goxdcr/service_def"
	service_def_mock "github.com/couchbase/goxdcr/service_def/mocks"
	xdcrUtils "github.com/couchbase/goxdcr/utils"
	"github.com/nelio2k/xdcrDiffer/base"
	"github.com/nelio2k/xdcrDiffer/dcp"
	"github.com/nelio2k/xdcrDiffer/differ"
	fdp "github.com/nelio2k/xdcrDiffer/fileDescriptorPool"
	"github.com/nelio2k/xdcrDiffer/utils"
	"os"
	"os/signal"
	"strings"
	"sync"
	"time"
)

var done = make(chan bool)

var options struct {
	sourceUrl                         string
	sourceUsername                    string
	sourcePassword                    string
	sourceBucketName                  string
	remoteClusterName                 string
	sourceFileDir                     string
	targetUrl                         string
	targetUsername                    string
	targetPassword                    string
	targetBucketName                  string
	targetFileDir                     string
	numberOfSourceDcpClients          uint64
	numberOfWorkersPerSourceDcpClient uint64
	numberOfTargetDcpClients          uint64
	numberOfWorkersPerTargetDcpClient uint64
	numberOfWorkersForFileDiffer      uint64
	numberOfWorkersForMutationDiffer  uint64
	numberOfBins                      uint64
	numberOfFileDesc                  uint64
	// the duration that the tools should be run, in minutes
	completeByDuration uint64
	// whether tool should complete after processing all mutations at tool start time
	completeBySeqno bool
	// directory for checkpoint files
	checkpointFileDir string
	// name of source cluster checkpoint file to load from when tool starts
	// if not specified, source cluster will start from 0
	oldSourceCheckpointFileName string
	// name of target cluster checkpoint file to load from when tool starts
	// if not specified, target cluster will start from 0
	oldTargetCheckpointFileName string
	// name of new checkpoint file to write to when tool shuts down
	// if not specified, tool will not save checkpoint files
	newCheckpointFileName string
	// directory for storing diffs generated by file differ
	fileDifferDir string
	// input directory for mutation differ
	// if this directory is not specified, it indicates that mutation differ is expected to read diff keys generated by file differ,
	// i.e., diffFileDir/base.DiffKeysFileName
	// if this directory is specified, it indicates that mutation differ is expected to read diff keys generated by mutation differ itself
	// i.e., inputDiffKeysFileDir/base.MutationDiffKeysFileName
	inputDiffKeysFileDir string
	// output directory for mutation differ
	mutationDifferDir string
	// size of batch used by mutation differ
	mutationDifferBatchSize uint64
	// timeout, in seconds, used by mutation differ
	mutationDifferTimeout uint64
	// size of source dcp handler channel
	sourceDcpHandlerChanSize uint64
	// size of target dcp handler channel
	targetDcpHandlerChanSize uint64
	// timeout for bucket for stats collection, in seconds
	bucketOpTimeout uint64
	// max number of retry for get stats
	maxNumOfGetStatsRetry uint64
	// max number of retry for send batch
	maxNumOfSendBatchRetry uint64
	// retry interval for get stats, in seconds
	getStatsRetryInterval uint64
	// retry interval for send batch, in milliseconds
	sendBatchRetryInterval uint64
	// max backoff for get stats, in seconds
	getStatsMaxBackoff uint64
	// max backoff for send batch, in seconds
	sendBatchMaxBackoff uint64
	// delay between source cluster start up and target cluster start up, in seconds
	delayBetweenSourceAndTarget uint64
	//interval for periodical checkpointing, in seconds
	// value of 0 indicates no periodical checkpointing
	checkpointInterval uint64
	// whether to run data generation
	runDataGeneration bool
	// whether to run file differ
	runFileDiffer bool
	// whether to verify diff keys through aysnc Get on clusters
	runMutationDiffer bool
}

func argParse() {
	flag.StringVar(&options.sourceUrl, "sourceUrl", "",
		"url for source cluster")
	flag.StringVar(&options.sourceUsername, "sourceUsername", "",
		"username for source cluster")
	flag.StringVar(&options.sourcePassword, "sourcePassword", "",
		"password for source cluster")
	flag.StringVar(&options.sourceBucketName, "sourceBucketName", "",
		"bucket name for source cluster")
	flag.StringVar(&options.remoteClusterName, "remoteClusterName", "",
		"Remote cluster reference name used when creating it")
	flag.StringVar(&options.sourceFileDir, "sourceFileDir", base.SourceFileDir,
		"directory to store mutations in source cluster")
	flag.StringVar(&options.targetUrl, "targetUrl", "",
		"url for target cluster")
	flag.StringVar(&options.targetUsername, "targetUsername", "",
		"username for target cluster")
	flag.StringVar(&options.targetPassword, "targetPassword", "",
		"password for target cluster")
	flag.StringVar(&options.targetBucketName, "targetBucketName", "",
		"bucket name for target cluster")
	flag.StringVar(&options.targetFileDir, "targetFileDir", base.TargetFileDir,
		"directory to store mutations in target cluster")
	flag.Uint64Var(&options.numberOfSourceDcpClients, "numberOfSourceDcpClients", 4,
		"number of source dcp clients")
	flag.Uint64Var(&options.numberOfWorkersPerSourceDcpClient, "numberOfWorkersPerSourceDcpClient", 256,
		"number of workers for each source dcp client")
	flag.Uint64Var(&options.numberOfTargetDcpClients, "numberOfTargetDcpClients", 4,
		"number of target dcp clients")
	flag.Uint64Var(&options.numberOfWorkersPerTargetDcpClient, "numberOfWorkersPerTargetDcpClient", 256,
		"number of workers for each target dcp client")
	flag.Uint64Var(&options.numberOfWorkersForFileDiffer, "numberOfWorkersForFileDiffer", 30,
		"number of worker threads for file differ ")
	flag.Uint64Var(&options.numberOfWorkersForMutationDiffer, "numberOfWorkersForMutationDiffer", 30,
		"number of worker threads for mutation differ ")
	flag.Uint64Var(&options.numberOfBins, "numberOfBins", 10,
		"number of buckets per vbucket")
	flag.Uint64Var(&options.numberOfFileDesc, "numberOfFileDesc", 500,
		"number of file descriptors")
	flag.Uint64Var(&options.completeByDuration, "completeByDuration", 0,
		"duration that the tool should run")
	flag.BoolVar(&options.completeBySeqno, "completeBySeqno", true,
		"whether tool should automatically complete (after processing all mutations at start time)")
	flag.StringVar(&options.checkpointFileDir, "checkpointFileDir", base.CheckpointFileDir,
		"directory for checkpoint files")
	flag.StringVar(&options.oldSourceCheckpointFileName, "oldSourceCheckpointFileName", "",
		"old source checkpoint file to load from when tool starts")
	flag.StringVar(&options.oldTargetCheckpointFileName, "oldTargetCheckpointFileName", "",
		"old target checkpoint file to load from when tool starts")
	flag.StringVar(&options.newCheckpointFileName, "newCheckpointFileName", "",
		"new checkpoint file to write to when tool shuts down")
	flag.StringVar(&options.fileDifferDir, "fileDifferDir", base.FileDifferDir,
		" directory for storing diffs generated by file differ")
	flag.StringVar(&options.inputDiffKeysFileDir, "inputDiffKeysFileDir", "",
		" directory to load diff key file to be used by mutation differ")
	flag.StringVar(&options.mutationDifferDir, "mutationDifferDir", base.MutationDifferDir,
		" output directory for mutation differ")
	flag.Uint64Var(&options.mutationDifferBatchSize, "mutationDifferBatchSize", 100,
		"size of batch used by mutation differ")
	flag.Uint64Var(&options.mutationDifferTimeout, "mutationDifferTimeout", 30,
		"timeout, in seconds, used by mutation differ")
	flag.Uint64Var(&options.sourceDcpHandlerChanSize, "sourceDcpHandlerChanSize", base.DcpHandlerChanSize,
		"size of source dcp handler channel")
	flag.Uint64Var(&options.targetDcpHandlerChanSize, "targetDcpHandlerChanSize", base.DcpHandlerChanSize,
		"size of target dcp handler channel")
	flag.Uint64Var(&options.bucketOpTimeout, "bucketOpTimeout", base.BucketOpTimeout,
		" timeout for bucket for stats collection, in seconds")
	flag.Uint64Var(&options.maxNumOfGetStatsRetry, "maxNumOfGetStatsRetry", base.MaxNumOfGetStatsRetry,
		"max number of retry for get stats")
	flag.Uint64Var(&options.maxNumOfSendBatchRetry, "maxNumOfSendBatchRetry", base.MaxNumOfSendBatchRetry,
		"max number of retry for send batch")
	flag.Uint64Var(&options.getStatsRetryInterval, "getStatsRetryInterval", base.GetStatsRetryInterval,
		" retry interval for get stats, in seconds")
	flag.Uint64Var(&options.sendBatchRetryInterval, "sendBatchRetryInterval", base.SendBatchRetryInterval,
		"retry interval for send batch, in milliseconds")
	flag.Uint64Var(&options.getStatsMaxBackoff, "getStatsMaxBackoff", base.GetStatsMaxBackoff,
		"max backoff for get stats, in seconds")
	flag.Uint64Var(&options.sendBatchMaxBackoff, "sendBatchMaxBackoff", base.SendBatchMaxBackoff,
		"max backoff for send batch, in seconds")
	flag.Uint64Var(&options.delayBetweenSourceAndTarget, "delayBetweenSourceAndTarget", base.DelayBetweenSourceAndTarget,
		"delay between source cluster start up and target cluster start up, in seconds")
	flag.Uint64Var(&options.checkpointInterval, "checkpointInterval", base.CheckpointInterval,
		"interval for periodical checkpointing, in seconds")
	flag.BoolVar(&options.runDataGeneration, "runDataGeneration", true,
		" whether to run data generation")
	flag.BoolVar(&options.runFileDiffer, "runFileDiffer", true,
		" whether to file differ")
	flag.BoolVar(&options.runMutationDiffer, "runMutationDiffer", true,
		" whether to verify diff keys through aysnc Get on clusters")

	flag.Parse()
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage : %s [OPTIONS] \n", os.Args[0])
	flag.PrintDefaults()
}

type diffToolStateType int

const (
	finStateInitial diffToolStateType = iota
	dcpDriving      diffToolStateType = iota
	finStateFinal   diffToolStateType = iota
)

type difftoolState struct {
	state diffToolStateType
	mtx   sync.Mutex
}

type xdcrDiffTool struct {
	utils              xdcrUtils.UtilsIface
	metadataSvc        service_def.MetadataSvc
	remoteClusterSvc   service_def.RemoteClusterSvc
	replicationSpecSvc service_def.ReplicationSpecSvc
	logger             *xdcrLog.CommonLogger

	specifiedRef  *metadata.RemoteClusterReference
	specifiedSpec *metadata.ReplicationSpecification
	filter        xdcrParts.FilterIface

	sourceDcpDriver *dcp.DcpDriver
	targetDcpDriver *dcp.DcpDriver

	curState difftoolState
	// finch - to interrupt one at a time
	//	generateDataFinch chan bool
}

func NewDiffTool() *xdcrDiffTool {

	difftool := &xdcrDiffTool{
		utils: xdcrUtils.NewUtilities(),
		//		generateDataFinch: make(chan bool),
	}
	difftool.metadataSvc, _ = metadata_svc.NewMetaKVMetadataSvc(nil, difftool.utils)

	uiLogSvcMock := &service_def_mock.UILogSvc{}
	xdcrTopologyMock := &service_def_mock.XDCRCompTopologySvc{}
	clusterInfoSvcMock := &service_def_mock.ClusterInfoSvc{}

	difftool.logger = xdcrLog.NewLogger("xdcrDiffTool", nil)

	difftool.remoteClusterSvc, _ = metadata_svc.NewRemoteClusterService(uiLogSvcMock, difftool.metadataSvc, xdcrTopologyMock,
		clusterInfoSvcMock, xdcrLog.DefaultLoggerContext, difftool.utils)

	difftool.replicationSpecSvc, _ = metadata_svc.NewReplicationSpecService(uiLogSvcMock, difftool.remoteClusterSvc,
		difftool.metadataSvc, xdcrTopologyMock, clusterInfoSvcMock,
		nil, difftool.utils)

	// Capture any Ctrl-C for continuing to next steps or cleanup
	go difftool.monitorInterruptSignal()

	return difftool
}

func maybeSetEnv(key, value string) {
	if os.Getenv(key) != "" {
		return
	}
	os.Setenv(key, value)
}

func main() {
	argParse()

	difftool := NewDiffTool()

	if len(options.remoteClusterName) > 0 {
		err := difftool.retrieveReplicationSpecInfo()
		if err != nil {
			os.Exit(1)
		}
	} else {
		difftool.populateTemporarySpecAndRef()
	}

	if options.runDataGeneration {
		err := difftool.generateDataFiles()
		if err != nil {
			fmt.Printf("Error generating data files. err=%v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("Skipping  generating data files since it has been disabled\n")
	}

	if options.runFileDiffer {
		err := difftool.diffDataFiles()
		if err != nil {
			fmt.Printf("Error running file difftool. err=%v\n", err)
			os.Exit(1)
		}
	} else {
		fmt.Printf("Skipping file difftool since it has been disabled\n")
	}

	if options.runMutationDiffer {
		difftool.runMutationDiffer()
	} else {
		fmt.Printf("Skipping mutation diff since it has been disabled\n")
	}
}

func cleanUpAndSetup() error {
	err := os.MkdirAll(options.sourceFileDir, 0777)
	if err != nil {
		fmt.Printf("Error mkdir targetFileDir: %v\n", err)
	}
	err = os.MkdirAll(options.targetFileDir, 0777)
	if err != nil {
		fmt.Printf("Error mkdir targetFileDir: %v\n", err)
	}
	err = os.MkdirAll(options.checkpointFileDir, 0777)
	if err != nil {
		// it is ok for checkpoint dir to be existing, since we do not clean it up
		fmt.Printf("Error mkdir checkpointFileDir: %v\n", err)
	}
	return nil
}

func (difftool *xdcrDiffTool) createFilterIfNecessary() error {
	var ok bool
	var expr string
	if expr, ok = difftool.specifiedSpec.Settings.Values[metadata.FilterExpressionKey].(string); !ok {
		return nil
	}

	var filterVersion xdcrBase.FilterVersionType
	if filterVersion, ok = difftool.specifiedSpec.Settings.Values[metadata.FilterVersionKey].(xdcrBase.FilterVersionType); !ok {
		err := fmt.Errorf("Unable to find filter version given filter expression %v\nsettings:%v\n", expr, difftool.specifiedSpec.Settings)
		return err
	}

	if filterVersion == xdcrBase.FilterVersionKeyOnly {
		expr = xdcrBase.UpgradeFilter(expr)
	}
	difftool.logger.Infof("Found filtering expression: %v\n", expr)

	filter, err := xdcrParts.NewFilter("XDCRDiffToolFilter", expr, difftool.utils)
	difftool.filter = filter
	return err
}

func (difftool *xdcrDiffTool) generateDataFiles() error {
	difftool.logger.Infof("GenerateDataFiles routine started\n")
	defer difftool.logger.Infof("GenerateDataFiles routine completed\n")

	if options.completeByDuration == 0 && !options.completeBySeqno {
		difftool.logger.Infof("completeByDuration is required when completeBySeqno is false\n")
		os.Exit(1)
	}

	difftool.logger.Infof("Tool started\n")

	if err := cleanUpAndSetup(); err != nil {
		difftool.logger.Errorf("Unable to clean and set up directory structure: %v\n", err)
		os.Exit(1)
	}

	errChan := make(chan error, 1)
	waitGroup := &sync.WaitGroup{}

	var fileDescPool fdp.FdPoolIface
	if options.numberOfFileDesc > 0 {
		fileDescPool = fdp.NewFileDescriptorPool(int(options.numberOfFileDesc))
	}

	if err := difftool.createFilterIfNecessary(); err != nil {
		os.Exit(1)
	}

	difftool.logger.Infof("Starting source dcp clients on %v\n", options.sourceUrl)
	difftool.sourceDcpDriver = startDcpDriver(difftool.logger, base.SourceClusterName, options.sourceUrl, difftool.specifiedSpec.SourceBucketName,
		options.sourceUsername, options.sourcePassword, options.sourceFileDir, options.checkpointFileDir,
		options.oldSourceCheckpointFileName, options.newCheckpointFileName, options.numberOfSourceDcpClients,
		options.numberOfWorkersPerSourceDcpClient, options.numberOfBins, options.sourceDcpHandlerChanSize,
		options.bucketOpTimeout, options.maxNumOfGetStatsRetry, options.getStatsRetryInterval,
		options.getStatsMaxBackoff, options.checkpointInterval, errChan, waitGroup, options.completeBySeqno, fileDescPool, difftool.filter)

	delayDurationBetweenSourceAndTarget := time.Duration(options.delayBetweenSourceAndTarget) * time.Second
	difftool.logger.Infof("Waiting for %v before starting target dcp clients\n", delayDurationBetweenSourceAndTarget)
	time.Sleep(delayDurationBetweenSourceAndTarget)

	difftool.logger.Infof("Starting target dcp clients\n")
	difftool.targetDcpDriver = startDcpDriver(difftool.logger, base.TargetClusterName, difftool.specifiedRef.HostName_, difftool.specifiedSpec.TargetBucketName,
		difftool.specifiedRef.UserName_, difftool.specifiedRef.Password_, options.targetFileDir, options.checkpointFileDir,
		options.oldTargetCheckpointFileName, options.newCheckpointFileName, options.numberOfTargetDcpClients,
		options.numberOfWorkersPerTargetDcpClient, options.numberOfBins, options.targetDcpHandlerChanSize,
		options.bucketOpTimeout, options.maxNumOfGetStatsRetry, options.getStatsRetryInterval,
		options.getStatsMaxBackoff, options.checkpointInterval, errChan, waitGroup, options.completeBySeqno, fileDescPool, difftool.filter)

	difftool.curState.mtx.Lock()
	difftool.curState.state = dcpDriving
	difftool.curState.mtx.Unlock()

	var err error
	if options.completeBySeqno {
		err = difftool.waitForCompletion(difftool.sourceDcpDriver, difftool.targetDcpDriver, errChan, waitGroup)
	} else {
		err = difftool.waitForDuration(difftool.sourceDcpDriver, difftool.targetDcpDriver, errChan, options.completeByDuration, delayDurationBetweenSourceAndTarget)
	}

	return err
}

func (difftool *xdcrDiffTool) diffDataFiles() error {
	difftool.logger.Infof("DiffDataFiles routine started\n")
	defer difftool.logger.Infof("DiffDataFiles routine completed\n")

	err := os.RemoveAll(options.fileDifferDir)
	if err != nil {
		difftool.logger.Errorf("Error removing fileDifferDir: %v\n", err)
	}
	err = os.MkdirAll(options.fileDifferDir, 0777)
	if err != nil {
		return fmt.Errorf("Error mkdir fileDifferDir: %v\n", err)
	}

	difftoolDriver := differ.NewDifferDriver(options.sourceFileDir, options.targetFileDir, options.fileDifferDir, base.DiffKeysFileName, int(options.numberOfWorkersForFileDiffer), int(options.numberOfBins), int(options.numberOfFileDesc))
	err = difftoolDriver.Run()
	if err != nil {
		difftool.logger.Errorf("Error from diffDataFiles = %v\n", err)
	}

	return err
}

func (difftool *xdcrDiffTool) runMutationDiffer() {
	difftool.logger.Infof("runMutationDiffer started\n")
	defer difftool.logger.Infof("runMutationDiffer completed\n")

	err := os.RemoveAll(options.mutationDifferDir)
	if err != nil {
		difftool.logger.Errorf("Error removing mutationDifferDir: %v\n", err)
	}
	err = os.MkdirAll(options.mutationDifferDir, 0777)
	if err != nil {
		err = fmt.Errorf("Error mkdir mutationDifferDir: %v\n", err)
		return
	}

	mutationDiffer := differ.NewMutationDiffer(options.sourceUrl, difftool.specifiedSpec.SourceBucketName, options.sourceUsername,
		options.sourcePassword, difftool.specifiedRef.HostName_, difftool.specifiedSpec.TargetBucketName, difftool.specifiedRef.UserName_,
		difftool.specifiedRef.Password_, options.fileDifferDir, options.mutationDifferDir, options.inputDiffKeysFileDir,
		int(options.numberOfWorkersForMutationDiffer), int(options.mutationDifferBatchSize), int(options.mutationDifferTimeout),
		int(options.maxNumOfSendBatchRetry), time.Duration(options.sendBatchRetryInterval)*time.Millisecond,
		time.Duration(options.sendBatchMaxBackoff)*time.Second, difftool.logger)
	err = mutationDiffer.Run()
	if err != nil {
		difftool.logger.Errorf("Error from runMutationDiffer = %v\n", err)
	}
}

func startDcpDriver(logger *xdcrLog.CommonLogger, name, url, bucketName, userName, password, fileDir, checkpointFileDir, oldCheckpointFileName,
	newCheckpointFileName string, numberOfDcpClients, numberOfWorkersPerDcpClient, numberOfBins,
	dcpHandlerChanSize, bucketOpTimeout, maxNumOfGetStatsRetry, getStatsRetryInterval, getStatsMaxBackoff,
	checkpointInterval uint64, errChan chan error, waitGroup *sync.WaitGroup, completeBySeqno bool,
	fdPool fdp.FdPoolIface, filter xdcrParts.FilterIface) *dcp.DcpDriver {
	waitGroup.Add(1)
	dcpDriver := dcp.NewDcpDriver(logger, name, url, bucketName, userName, password, fileDir, checkpointFileDir, oldCheckpointFileName,
		newCheckpointFileName, int(numberOfDcpClients), int(numberOfWorkersPerDcpClient), int(numberOfBins),
		int(dcpHandlerChanSize), time.Duration(bucketOpTimeout)*time.Second, int(maxNumOfGetStatsRetry),
		time.Duration(getStatsRetryInterval)*time.Second, time.Duration(getStatsMaxBackoff)*time.Second,
		int(checkpointInterval), errChan, waitGroup, completeBySeqno, fdPool, filter)
	// dcp driver startup may take some time. Do it asynchronously
	go startDcpDriverAysnc(dcpDriver, errChan, logger)
	return dcpDriver
}

func startDcpDriverAysnc(dcpDriver *dcp.DcpDriver, errChan chan error, logger *xdcrLog.CommonLogger) {
	err := dcpDriver.Start()
	if err != nil {
		logger.Errorf("Error starting dcp driver %v. err=%v\n", dcpDriver.Name, err)
		utils.AddToErrorChan(errChan, err)
	}
}

func (difftool *xdcrDiffTool) waitForCompletion(sourceDcpDriver, targetDcpDriver *dcp.DcpDriver, errChan chan error, waitGroup *sync.WaitGroup) error {
	doneChan := make(chan bool, 1)
	go utils.WaitForWaitGroup(waitGroup, doneChan)

	select {
	case err := <-errChan:
		difftool.logger.Errorf("Stop diff generation due to error from dcp client %v\n", err)
		err1 := sourceDcpDriver.Stop()
		if err1 != nil {
			difftool.logger.Errorf("Error stopping source dcp client. err=%v\n", err1)
		}
		err1 = targetDcpDriver.Stop()
		if err1 != nil {
			difftool.logger.Errorf("Error stopping target dcp client. err=%v\n", err1)
		}
		return err
	case <-doneChan:
		difftool.logger.Infof("Source cluster and target cluster have completed\n")
		return nil
	}

	return nil
}

func (difftool *xdcrDiffTool) waitForDuration(sourceDcpDriver, targetDcpDriver *dcp.DcpDriver, errChan chan error, duration uint64, delayDurationBetweenSourceAndTarget time.Duration) (err error) {
	timer := time.NewTimer(time.Duration(duration) * time.Second)

	select {
	case err = <-errChan:
		difftool.logger.Errorf("Stop diff generation due to error from dcp client %v\n", err)
	case <-timer.C:
		difftool.logger.Infof("Stop diff generation after specified processing duration\n")
	}

	err1 := sourceDcpDriver.Stop()
	if err1 != nil {
		difftool.logger.Errorf("Error stopping source dcp client. err=%v\n", err1)
	}

	time.Sleep(delayDurationBetweenSourceAndTarget)

	err1 = targetDcpDriver.Stop()
	if err1 != nil {
		difftool.logger.Errorf("Error stopping target dcp client. err=%v\n", err1)
	}

	return err
}

func (difftool *xdcrDiffTool) retrieveReplicationSpecInfo() error {
	// CBAUTH has already been setup
	rcMap, err := difftool.remoteClusterSvc.RemoteClusters()
	if err != nil {
		difftool.logger.Errorf("Error retrieving remote clusters: %v\n", err)
		return err
	}

	specMap, err := difftool.replicationSpecSvc.AllReplicationSpecs()
	if err != nil {
		difftool.logger.Errorf("Error retrieving specs: %v\n", err)
	}

	for _, ref := range rcMap {
		if ref.Name_ == options.remoteClusterName {
			difftool.specifiedRef = ref
			break
		}
	}

	for _, spec := range specMap {
		if spec.SourceBucketName == options.sourceBucketName && spec.TargetBucketName == options.targetBucketName {
			difftool.specifiedSpec = spec
			break
		}
	}

	var errStrs []string
	if difftool.specifiedRef == nil {
		errStrs = append(errStrs, fmt.Sprintf("Unable to find Remote cluster %v\n", options.remoteClusterName))
	}
	if difftool.specifiedSpec == nil {
		errStrs = append(errStrs, fmt.Sprintf("Unable to find Replication Spec with source %v target %v\n", options.sourceBucketName, options.targetBucketName))
	}
	if len(errStrs) > 0 {
		err := fmt.Errorf(strings.Join(errStrs, " and "))
		difftool.logger.Errorf(err.Error())
		return err
	}

	difftool.logger.Infof("Found Remote Cluster: %v and Replication Spec: %v\n", difftool.specifiedRef.String(), difftool.specifiedSpec.String())

	return nil
}

func (difftool *xdcrDiffTool) populateTemporarySpecAndRef() {
	difftool.specifiedSpec, _ = metadata.NewReplicationSpecification(options.sourceBucketName, "", /*sourceBucketUUID*/
		"" /*targetClusterUUID*/, options.targetBucketName, "" /*targetBucketUUID*/)

	difftool.specifiedRef, _ = metadata.NewRemoteClusterReference("" /*uuid*/, "" /*name*/, options.targetUrl, options.targetUsername, options.targetPassword,
		false /*demandEncryption*/, "" /*encryptionType*/, nil, nil, nil)
}

func (difftool *xdcrDiffTool) monitorInterruptSignal() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		for sig := range c {
			if sig.String() == "interrupt" {
				difftool.curState.mtx.Lock()
				switch difftool.curState.state {
				case finStateInitial:
					os.Exit(0)
				case dcpDriving:
					difftool.logger.Warnf("Received interrupt. Closing DCP drivers")
					difftool.sourceDcpDriver.Stop()
					difftool.targetDcpDriver.Stop()
					difftool.curState.state = finStateFinal
				case finStateFinal:
					os.Exit(0)
				}
				difftool.curState.mtx.Unlock()
			}
		}
	}()
}
