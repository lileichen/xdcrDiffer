// Copyright (c) 2018 Couchbase, Inc.
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
// except in compliance with the License. You may obtain a copy of the License at
//   http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under the
// License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing permissions
// and limitations under the License.

package differ

import (
	"encoding/json"
	"fmt"
	"github.com/couchbase/gocb"
	"github.com/nelio2k/xdcrDiffer/base"
	"github.com/nelio2k/xdcrDiffer/utils"
	gocbcore "gopkg.in/couchbase/gocbcore.v7"
	"io/ioutil"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

const KeyNotFoundErrMsg = "key not found"

type MutationDiffer struct {
	sourceUrl        string
	sourceBucketName string
	sourceUserName   string
	sourcePassword   string
	targetUrl        string
	targetBucketName string
	targetUserName   string
	targetPassword   string
	diffFileDir      string
	numberOfWorkers  int

	sourceBucket *gocb.Bucket
	targetBucket *gocb.Bucket

	missingFromSource map[string]*gocbcore.GetMetaResult
	missingFromTarget map[string]*gocbcore.GetMetaResult
	diff              map[string][]*gocbcore.GetMetaResult
	stateLock         *sync.RWMutex
}

type DifferWorker struct {
	differ *MutationDiffer
	// keys to do diff on
	keys              []string
	sourceBucket      *gocb.Bucket
	targetBucket      *gocb.Bucket
	waitGroup         *sync.WaitGroup
	sourceResultCount uint32
	targetResultCount uint32
}

func NewMutationDiffer(sourceUrl string,
	sourceBucketName string,
	sourceUserName string,
	sourcePassword string,
	targetUrl string,
	targetBucketName string,
	targetUserName string,
	targetPassword string,
	diffFileDir string,
	numberOfWorkers int) *MutationDiffer {
	return &MutationDiffer{
		sourceUrl:         sourceUrl,
		sourceBucketName:  sourceBucketName,
		sourceUserName:    sourceUserName,
		sourcePassword:    sourcePassword,
		targetUrl:         targetUrl,
		targetBucketName:  targetBucketName,
		targetUserName:    targetUserName,
		targetPassword:    targetPassword,
		diffFileDir:       diffFileDir,
		numberOfWorkers:   numberOfWorkers,
		missingFromSource: make(map[string]*gocbcore.GetMetaResult),
		missingFromTarget: make(map[string]*gocbcore.GetMetaResult),
		diff:              make(map[string][]*gocbcore.GetMetaResult),
		stateLock:         &sync.RWMutex{},
	}
}

func (d *MutationDiffer) Run() error {
	diffKeys, err := d.loadDiffKeys()
	if err != nil {
		return err
	}

	err = d.initialize()
	if err != nil {
		return err
	}

	loadDistribution := utils.BalanceLoad(d.numberOfWorkers, len(diffKeys))
	waitGroup := &sync.WaitGroup{}
	for i := 0; i < d.numberOfWorkers; i++ {
		lowIndex := loadDistribution[i][0]
		highIndex := loadDistribution[i][1]
		if lowIndex == highIndex {
			// skip workers with 0 load
			continue
		}
		diffWorker := NewDifferWorker(d, d.sourceBucket, d.targetBucket, diffKeys[lowIndex:highIndex], waitGroup)
		waitGroup.Add(1)
		go diffWorker.run()
	}

	waitGroup.Wait()

	d.writeDiff()

	return nil
}

func (d *MutationDiffer) writeDiff() error {
	diffBytes, err := d.getDiffBytes()
	if err != nil {
		return err
	}

	return d.writeDiffBytesToFile(diffBytes)
}

func (d *MutationDiffer) getDiffBytes() ([]byte, error) {
	outputMap := map[string]interface{}{
		"Mismatch":          d.diff,
		"MissingFromSource": d.missingFromSource,
		"MissingFromTarget": d.missingFromTarget,
	}

	return json.Marshal(outputMap)
}

func (d *MutationDiffer) writeDiffBytesToFile(diffBytes []byte) error {
	diffFileName := d.diffFileDir + base.FileDirDelimiter + base.MutationDiffFileName
	diffFile, err := os.OpenFile(diffFileName, os.O_RDWR|os.O_CREATE, base.FileModeReadWrite)
	if err != nil {
		return err
	}

	defer diffFile.Close()

	_, err = diffFile.Write(diffBytes)
	return err

}

func (d *MutationDiffer) loadDiffKeys() ([]string, error) {
	diffKeysFileName := d.diffFileDir + base.FileDirDelimiter + base.DiffKeysFileName
	diffKeysBytes, err := ioutil.ReadFile(diffKeysFileName)
	if err != nil {
		return nil, err
	}

	diffKeys := make([]string, 0)
	err = json.Unmarshal(diffKeysBytes, &diffKeys)
	if err != nil {
		return nil, err
	}
	return diffKeys, nil
}

func (d *MutationDiffer) addDiff(missingFromSource map[string]*gocbcore.GetMetaResult,
	missingFromTarget map[string]*gocbcore.GetMetaResult,
	diff map[string][]*gocbcore.GetMetaResult) {
	d.stateLock.Lock()
	defer d.stateLock.Unlock()

	for key, result := range missingFromSource {
		d.missingFromSource[key] = result
	}
	for key, result := range missingFromTarget {
		d.missingFromTarget[key] = result
	}
	for key, results := range diff {
		d.diff[key] = results
	}
}

func NewDifferWorker(differ *MutationDiffer, sourceBucket, targetBucket *gocb.Bucket, keys []string, waitGroup *sync.WaitGroup) *DifferWorker {
	return &DifferWorker{
		differ:       differ,
		sourceBucket: sourceBucket,
		targetBucket: targetBucket,
		keys:         keys,
		waitGroup:    waitGroup,
	}
}

func (dw *DifferWorker) run() {
	defer dw.waitGroup.Done()
	sourceResults, targetResults := dw.getResults()
	dw.diff(sourceResults, targetResults)
}

func (dw *DifferWorker) getResults() (map[string]*GetResult, map[string]*GetResult) {

	sourceResults := make(map[string]*GetResult)
	targetResults := make(map[string]*GetResult)
	for _, key := range dw.keys {
		sourceResults[key] = &GetResult{}
		targetResults[key] = &GetResult{}
	}

	for _, key := range dw.keys {
		dw.get(key, sourceResults, true /*isSource*/)
		dw.get(key, targetResults, false /*isSource*/)
	}

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	timer := time.NewTimer(20 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ticker.C:
			if atomic.LoadUint32(&dw.sourceResultCount) == uint32(len(dw.keys)) &&
				atomic.LoadUint32(&dw.targetResultCount) == uint32(len(dw.keys)) {
				goto done
			}
		case <-timer.C:
			fmt.Printf("get timed out\n")
			goto done
		}
	}
done:
	return sourceResults, targetResults
}

func (dw *DifferWorker) diff(sourceResults, targetResults map[string]*GetResult) {
	missingFromSource := make(map[string]*gocbcore.GetMetaResult)
	missingFromTarget := make(map[string]*gocbcore.GetMetaResult)
	diff := make(map[string][]*gocbcore.GetMetaResult)

	for key, sourceResult := range sourceResults {
		if sourceResult.Key == "" {
			fmt.Printf("Skipping diff on %v since we did not get results from source\n", key)
			continue
		}

		targetResult := targetResults[key]
		if targetResult.Key == "" {
			fmt.Printf("Skipping diff on %v since we did not get results from target\n", key)
			continue
		}

		if isKeyNotFoundError(sourceResult.Error) && !isKeyNotFoundError(targetResult.Error) {
			missingFromSource[key] = targetResult.Result
			continue
		}
		if !isKeyNotFoundError(sourceResult.Error) && isKeyNotFoundError(targetResult.Error) {
			missingFromTarget[key] = sourceResult.Result
			continue
		}
		if !areGetMetaResultsTheSame(sourceResult.Result, targetResult.Result) {
			diff[key] = []*gocbcore.GetMetaResult{sourceResult.Result, targetResult.Result}
		}
	}

	dw.differ.addDiff(missingFromSource, missingFromTarget, diff)
}

func isKeyNotFoundError(err error) bool {
	return err != nil && err.Error() == KeyNotFoundErrMsg
}

func areGetMetaResultsTheSame(result1, result2 *gocbcore.GetMetaResult) bool {
	if result1 == nil {
		return result2 == nil
	}
	if result2 == nil {
		return false
	}
	return reflect.DeepEqual(result1.Value, result2.Value) && result1.Flags == result2.Flags &&
		result1.Datatype == result2.Datatype && result1.Cas == result2.Cas && result1.Expiry == result2.Expiry &&
		result1.SeqNo == result2.SeqNo && result1.Deleted == result2.Deleted
}

func (dw *DifferWorker) get(key string, resultsMap map[string]*GetResult, isSource bool) {
	getCallbackFunc := func(result *gocbcore.GetMetaResult, err error) {
		resultsMap[key].Key = string(key)
		resultsMap[key].Result = result
		resultsMap[key].Error = err
		if isSource {
			atomic.AddUint32(&dw.sourceResultCount, 1)
		} else {
			atomic.AddUint32(&dw.targetResultCount, 1)
		}
	}

	if isSource {
		dw.sourceBucket.IoRouter().GetMetaEx(gocbcore.GetMetaOptions{Key: []byte(key)}, getCallbackFunc)
	} else {
		dw.targetBucket.IoRouter().GetMetaEx(gocbcore.GetMetaOptions{Key: []byte(key)}, getCallbackFunc)
	}
}

type GetResult struct {
	Key    string
	Result *gocbcore.GetMetaResult
	Error  error
}

func (r *GetResult) String() string {
	if r.Result == nil {
		return fmt.Sprintf("nil result")
	}
	return fmt.Sprintf("Cas=%v Datatype=%v Flags=%v Value=%v", r.Result.Cas, r.Result.Datatype, r.Result.Flags, r.Result.Value)
}

func (d *MutationDiffer) initialize() error {
	var err error
	d.sourceBucket, err = d.openBucket(d.sourceUrl, d.sourceBucketName, d.sourceUserName, d.sourcePassword)
	if err != nil {
		return err
	}
	d.targetBucket, err = d.openBucket(d.targetUrl, d.targetBucketName, d.targetUserName, d.targetPassword)
	if err != nil {
		return err
	}
	return nil
}

func (d *MutationDiffer) openBucket(url, bucketName, username, password string) (*gocb.Bucket, error) {
	cluster, err := gocb.Connect(url)
	if err != nil {
		fmt.Printf("Error connecting to cluster %v. err=%v\n", url, err)
		return nil, err
	}

	err = cluster.Authenticate(gocb.PasswordAuthenticator{
		Username: username,
		Password: password,
	})

	if err != nil {
		fmt.Printf(err.Error())
		return nil, err
	}

	return cluster.OpenBucket(bucketName, "")
}
