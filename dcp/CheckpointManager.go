package dcp

import (
	"encoding/json"
	"fmt"
	"github.com/couchbase/gocb"
	"github.com/nelio2k/xdcrDiffer/base"
	"github.com/nelio2k/xdcrDiffer/utils"
	"io/ioutil"
	"math"
	"os"
	"sync"
	"time"
)

type CheckpointManager struct {
	dcpDriver             *DcpDriver
	clusterName           string
	oldCheckpointFileName string
	newCheckpointFileName string
	cluster               *gocb.Cluster
	startVBTS             map[uint16]*VBTS
	vbuuidMap             map[uint16]uint64
	seqnoMap              map[uint16]*SeqnoWithLock
	snapshots             map[uint16]*Snapshot
	endSeqnoMap           map[uint16]uint64
	finChan               chan bool
	bucketOpTimeout       time.Duration
	maxNumOfGetStatsRetry int
	getStatsRetryInterval time.Duration
	getStatsMaxBackoff    time.Duration
	checkpointInterval    int
	started               bool
	stateLock             sync.RWMutex
}

func NewCheckpointManager(dcpDriver *DcpDriver, checkpointFileDir, oldCheckpointFileName, newCheckpointFileName, clusterName string,
	bucketOpTimeout time.Duration, maxNumOfGetStatsRetry int, getStatsRetryInterval, getStatsMaxBackoff time.Duration,
	checkpointInterval int) *CheckpointManager {
	cm := &CheckpointManager{
		dcpDriver:             dcpDriver,
		clusterName:           clusterName,
		startVBTS:             make(map[uint16]*VBTS),
		seqnoMap:              make(map[uint16]*SeqnoWithLock),
		snapshots:             make(map[uint16]*Snapshot),
		finChan:               make(chan bool),
		endSeqnoMap:           make(map[uint16]uint64),
		bucketOpTimeout:       bucketOpTimeout,
		maxNumOfGetStatsRetry: maxNumOfGetStatsRetry,
		getStatsRetryInterval: getStatsRetryInterval,
		getStatsMaxBackoff:    getStatsMaxBackoff,
		checkpointInterval:    checkpointInterval,
	}

	if checkpointFileDir != "" {
		if oldCheckpointFileName != "" {
			cm.oldCheckpointFileName = checkpointFileDir + base.FileDirDelimiter + clusterName + base.FileNameDelimiter + oldCheckpointFileName
		}

		if newCheckpointFileName != "" {
			cm.newCheckpointFileName = checkpointFileDir + base.FileDirDelimiter + clusterName + base.FileNameDelimiter + newCheckpointFileName
		}
	}

	var vbno uint16
	for vbno = 0; vbno < base.NumberOfVbuckets; vbno++ {
		cm.seqnoMap[vbno] = &SeqnoWithLock{}
		cm.snapshots[vbno] = &Snapshot{}
	}

	return cm
}

func (cm *CheckpointManager) Start() error {
	err := cm.initialize()
	if err != nil {
		return err
	}

	if cm.checkpointInterval > 0 {
		go cm.periodicalCheckpointing()
	}

	go cm.reportStatus()

	cm.setStarted()

	return nil
}

func (cm *CheckpointManager) setStarted() {
	cm.stateLock.Lock()
	defer cm.stateLock.Unlock()
	cm.started = true
}

func (cm *CheckpointManager) isStarted() bool {
	cm.stateLock.RLock()
	defer cm.stateLock.RUnlock()
	return cm.started
}

func (cm *CheckpointManager) Stop() error {
	fmt.Printf("CheckpointManager stopping\n")
	defer fmt.Printf("CheckpointManager stopped\n")

	if cm.isStarted() {
		err := cm.SaveCheckpoint()
		if err != nil {
			fmt.Printf("%v error saving checkpoint. err=%v\n", cm.clusterName, err)
		}
	}

	close(cm.finChan)

	return nil
}

func (cm *CheckpointManager) periodicalCheckpointing() {
	ticker := time.NewTicker(time.Duration(cm.checkpointInterval) * time.Second)
	defer ticker.Stop()

	// periodical checkpointing iteration
	// it is appended to checkpoint file name to make file name unique
	iter := 0

	for {
		select {
		case <-ticker.C:
			cm.checkpointOnce(iter)
			iter++
		case <-cm.finChan:
			return
		}
	}
}

func (cm *CheckpointManager) checkpointOnce(iter int) error {
	checkpointFileName := cm.newCheckpointFileName + base.FileNameDelimiter + fmt.Sprintf("%v", iter)
	return cm.saveCheckpoint(checkpointFileName)
}

func (cm *CheckpointManager) reportStatus() {
	ticker := time.NewTicker(time.Duration(base.StatsReportInterval) * time.Second)
	defer ticker.Stop()

	var prevSum uint64 = math.MaxUint64

	for {
		select {
		case <-ticker.C:
			prevSum = cm.reportStatusOnce(prevSum)
		case <-cm.finChan:
			return
		}
	}
}

func (cm *CheckpointManager) reportStatusOnce(prevSum uint64) uint64 {
	var vbno uint16
	var sum uint64
	for vbno = 0; vbno < base.NumberOfVbuckets; vbno++ {
		sum += cm.seqnoMap[vbno].getSeqno()
	}
	if prevSum != math.MaxUint64 {
		fmt.Printf("%v %v processed %v mutations. processing rate=%v mutation/second\n", time.Now(), cm.clusterName, sum, (sum-prevSum)/base.StatsReportInterval)
	} else {
		fmt.Printf("%v %v processed %v mutations.\n", time.Now(), cm.clusterName, sum)
	}
	return sum
}

func (cm *CheckpointManager) initialize() error {
	err := cm.initializeCluster()
	if err != nil {
		return err
	}

	err = cm.getVbuuidsAndHighSeqnos()
	if err != nil {
		return err
	}

	fmt.Printf("%v endSeqno map retrieved.\n", cm.clusterName)

	return cm.setStartVBTS()
}

func (cm *CheckpointManager) initializeCluster() error {
	cluster, err := gocb.Connect(cm.dcpDriver.url)
	if err != nil {
		fmt.Printf("%v error connecting to cluster %v. err=%v\n", cm.clusterName, cm.dcpDriver.url, err)
		return err
	}

	if cm.dcpDriver.rbacSupported {
		err = cluster.Authenticate(gocb.PasswordAuthenticator{
			Username: cm.dcpDriver.userName,
			Password: cm.dcpDriver.password,
		})

		if err != nil {
			fmt.Printf("%v error authenticating cluster. err=%v\n", cm.clusterName, err)
			return err
		}
	}

	cm.cluster = cluster

	return nil
}

func (cm *CheckpointManager) getVbuuidsAndHighSeqnos() error {
	statsBucket, err := cm.cluster.OpenBucket(cm.dcpDriver.bucketName, cm.dcpDriver.bucketPassword)
	if err != nil {
		fmt.Printf("%v error opening bucket. err=%v\n", cm.clusterName, err)
		return err
	}
	defer statsBucket.Close()

	statsMap, err := cm.getStatsWithRetry(statsBucket)
	if err != nil {
		return err
	}

	vbuuidMap := make(map[uint16]uint64)
	endSeqnoMap := make(map[uint16]uint64)
	err = utils.ParseHighSeqnoStat(statsMap, endSeqnoMap, vbuuidMap, true)
	if err != nil {
		return err
	}

	var sum uint64
	for _, seqno := range endSeqnoMap {
		sum += seqno
	}
	fmt.Printf("%v total docs=%v\n", cm.clusterName, sum)

	cm.vbuuidMap = vbuuidMap

	if cm.dcpDriver.completeBySeqno {
		cm.endSeqnoMap = endSeqnoMap
	} else {
		cm.endSeqnoMap = make(map[uint16]uint64)
		// set endSeqno to maxInt
		var vbno uint16
		for vbno = 0; vbno < base.NumberOfVbuckets; vbno++ {
			cm.endSeqnoMap[vbno] = math.MaxUint64
		}
	}

	return nil
}

// get stats is likely to time out. add retry
func (cm *CheckpointManager) getStatsWithRetry(statsBucket *gocb.Bucket) (map[string]map[string]string, error) {
	var statsMap map[string]map[string]string
	var err error
	getStatsFunc := func() error {
		statsMap, err = statsBucket.Stats(base.VbucketSeqnoStatName)
		return err
	}

	opErr := utils.ExponentialBackoffExecutor("getStatsWithRetry", cm.getStatsRetryInterval, cm.maxNumOfGetStatsRetry,
		base.GetStatsBackoffFactor, cm.getStatsMaxBackoff, getStatsFunc)
	if opErr != nil {
		return nil, opErr
	} else {
		return statsMap, nil
	}
}

func (cm *CheckpointManager) setStartVBTS() error {
	if cm.oldCheckpointFileName != "" {
		checkpointDoc, err := cm.loadCheckpoints()
		if err != nil {
			return err
		}
		for vbno, checkpoint := range checkpointDoc.Checkpoints {
			cm.startVBTS[vbno] = &VBTS{
				Checkpoint: checkpoint,
				EndSeqno:   cm.endSeqnoMap[vbno],
			}
			if cm.dcpDriver.completeBySeqno && checkpoint.Seqno >= cm.endSeqnoMap[vbno] {
				cm.startVBTS[vbno].NoNeedToStartDcpStream = true
			}

			// update start seqno as that in checkpoint doc
			cm.seqnoMap[vbno].setSeqno(checkpoint.Seqno)

		}
	} else {
		var vbno uint16
		for vbno = 0; vbno < base.NumberOfVbuckets; vbno++ {
			// if we are not loading checkpoints, it is ok to leave all fields in Checkpoint with default values, 0
			cm.startVBTS[vbno] = &VBTS{
				Checkpoint: &Checkpoint{},
				EndSeqno:   cm.endSeqnoMap[vbno],
			}
		}
	}

	return nil
}

func (cm *CheckpointManager) GetStartVBTS(vbno uint16) *VBTS {
	return cm.startVBTS[vbno]
}

func (cm *CheckpointManager) loadCheckpoints() (*CheckpointDoc, error) {
	checkpointFileBytes, err := ioutil.ReadFile(cm.oldCheckpointFileName)
	if err != nil {
		fmt.Printf("Error opening checkpoint file. err=%v\n", err)
		return nil, err
	}

	checkpointDoc := &CheckpointDoc{}
	err = json.Unmarshal(checkpointFileBytes, checkpointDoc)
	if err != nil {
		fmt.Printf("Error unmarshalling checkpoint file. err=%v\n", err)
		return nil, err
	}

	if len(checkpointDoc.Checkpoints) < base.NumberOfVbuckets {
		return nil, fmt.Errorf("checkpoint file %v has less than 1024 vbuckets.", cm.oldCheckpointFileName)
	}

	return checkpointDoc, nil
}

func (cm *CheckpointManager) SaveCheckpoint() error {
	if cm.newCheckpointFileName == "" {
		// checkpointing disabled
		fmt.Printf("Skipping checkpointing for %v since checkpointing has been disabled\n", cm.clusterName)
		return nil
	}
	return cm.saveCheckpoint(cm.newCheckpointFileName)
}

func (cm *CheckpointManager) saveCheckpoint(checkpointFileName string) error {

	// delete existing file if exists
	os.Remove(checkpointFileName)

	checkpointDoc := &CheckpointDoc{
		Checkpoints: make(map[uint16]*Checkpoint),
	}

	var vbno uint16
	var total uint64
	for vbno = 0; vbno < base.NumberOfVbuckets; vbno++ {
		vbuuid := cm.vbuuidMap[vbno]
		seqno := cm.seqnoMap[vbno].getSeqno()
		total += seqno
		var snapshotStartSeqno uint64
		var snapshotEndSeqno uint64

		curStartVBTS := cm.startVBTS[vbno].Checkpoint
		if seqno != curStartVBTS.Seqno {
			snapshotStartSeqno, snapshotEndSeqno = cm.getSnapshot(vbno)
		} else {
			// if we have not made any progress since start VBTS, use the same snapshotSeqnos as those in start VBTS
			snapshotStartSeqno = curStartVBTS.SnapshotStartSeqno
			snapshotEndSeqno = curStartVBTS.SnapshotEndSeqno
		}
		checkpointDoc.Checkpoints[vbno] = &Checkpoint{
			Vbuuid:             vbuuid,
			Seqno:              seqno,
			SnapshotStartSeqno: snapshotStartSeqno,
			SnapshotEndSeqno:   snapshotEndSeqno,
		}
	}

	value, err := json.Marshal(checkpointDoc)
	if err != nil {
		return err
	}

	checkpointFile, err := os.OpenFile(checkpointFileName, os.O_RDWR|os.O_CREATE, base.FileModeReadWrite)
	if err != nil {
		return err
	}

	defer checkpointFile.Close()

	numOfBytes, err := checkpointFile.Write(value)
	if err != nil {
		return err
	}
	if numOfBytes != len(value) {
		return fmt.Errorf("Incomplete write. expected=%v, actual=%v", len(value), numOfBytes)
	}

	fmt.Printf("----------------------------------------------------------------\n")
	fmt.Printf("%v saved checkpoints to %v. totalMutationsChecked=%v\n", cm.clusterName, checkpointFileName, total)
	return nil
}

// no need to lock seqoMap since
// 1. MutationProcessedEvent on a vbno are serialized
// 2. checkpointManager reads seqnoMap when it saves checkpoints.
//    This is done after all DcpHandlers are stopped and MutationProcessedEvent cease to happen
func (cm *CheckpointManager) HandleMutationEvent(mut *Mutation) bool {
	if cm.dcpDriver.completeBySeqno {
		endSeqno := cm.endSeqnoMap[mut.vbno]
		if mut.seqno >= endSeqno {
			cm.dcpDriver.handleVbucketCompletion(mut.vbno, nil, "end seqno reached")
		}
		if mut.seqno <= endSeqno {
			cm.seqnoMap[mut.vbno].setSeqno(mut.seqno)
			return true
		} else {
			return false
		}
	} else {
		cm.seqnoMap[mut.vbno].setSeqno(mut.seqno)
		return true
	}
}

func (cm *CheckpointManager) updateSnapshot(vbno uint16, startSeqno, endSeqno uint64) {
	snapshot := cm.snapshots[vbno]
	snapshot.lock.Lock()
	defer snapshot.lock.Unlock()

	snapshot.startSeqno = startSeqno
	snapshot.endSeqno = endSeqno
}

func (cm *CheckpointManager) getSnapshot(vbno uint16) (startSeqno, endSeqno uint64) {
	snapshot := cm.snapshots[vbno]
	snapshot.lock.RLock()
	defer snapshot.lock.RUnlock()

	return snapshot.startSeqno, snapshot.endSeqno
}

type Snapshot struct {
	startSeqno uint64
	endSeqno   uint64
	lock       sync.RWMutex
}

type SeqnoWithLock struct {
	Seqno uint64
	Lock  sync.RWMutex
}

func (s *SeqnoWithLock) setSeqno(seqno uint64) {
	s.Lock.Lock()
	defer s.Lock.Unlock()
	s.Seqno = seqno
}

func (s *SeqnoWithLock) getSeqno() uint64 {
	s.Lock.RLock()
	defer s.Lock.RUnlock()
	return s.Seqno
}
