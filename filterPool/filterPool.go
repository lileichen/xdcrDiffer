// Copyright (c) 2023 Couchbase, Inc.
// Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file
// except in compliance with the License. You may obtain a copy of the License at
//   http://www.apache.org/licenses/LICENSE-2.0
// Unless required by applicable law or agreed to in writing, software distributed under the
// License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND,
// either express or implied. See the License for the specific language governing permissions
// and limitations under the License.

package filterPool

import (
	"fmt"
	xdcrBase "github.com/couchbase/goxdcr/base"
	xdcrParts "github.com/couchbase/goxdcr/base/filter"
	xdcrUtils "github.com/couchbase/goxdcr/utils"
)

// Implements xdcrParts/filter

type filterWithState struct {
	filter xdcrParts.Filter
	myIdx  int
}

type FilterPool struct {
	dataPool    xdcrBase.DataPool
	filtersList []*filterWithState
	tokenCh     chan int
}

func (f *FilterPool) FilterUprEvent(wrappedUprEvent *xdcrBase.WrappedUprEvent) (bool, error, string, int64) {
	// Get an index token to use
	idxToUse := <-f.tokenCh
	// Ensure that the index is returned at the end for reuse
	defer func() {
		f.tokenCh <- idxToUse
	}()

	return f.filtersList[idxToUse].filter.FilterUprEvent(wrappedUprEvent)
}

func (f *FilterPool) SetShouldSkipUncommittedTxn(val bool) {
	// This needs to globally set everything
	// maybe revisit this? For now it's not used for differ and this seems to be fine
	for i := 0; i < len(f.filtersList); i++ {
		f.filtersList[i].filter.SetShouldSkipUncommittedTxn(val)
	}
}

func NewFilterPool(numOfFilters int, expr string, utils xdcrUtils.UtilsIface, skipUncommittedTxn bool) (*FilterPool, error) {
	fp := &FilterPool{
		dataPool:    xdcrBase.NewDataPool(),
		filtersList: make([]*filterWithState, numOfFilters, numOfFilters),
		tokenCh:     make(chan int, numOfFilters),
	}

	for i := 0; i < numOfFilters; i++ {
		filter, err := xdcrParts.NewFilter(fmt.Sprintf("XDCRDiffToolFilter_%v", i), expr, utils, skipUncommittedTxn)
		if err != nil {
			return nil, err
		}

		fs := &filterWithState{
			filter: filter,
			myIdx:  i,
		}
		fp.filtersList[i] = fs
		// When initialized, this index is available for work
		fp.tokenCh <- i
	}
	return fp, nil
}
