// Copyright 2015 The Cockroach Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or
// implied. See the License for the specific language governing
// permissions and limitations under the License. See the AUTHORS file
// for names of contributors.
//
// Author: Bram Gruneir (bram+code@cockroachlabs.com)

package storage_test

import (
	"bytes"
	"reflect"
	"testing"

	"golang.org/x/net/context"

	"github.com/cockroachdb/cockroach/keys"
	"github.com/cockroachdb/cockroach/proto"
	"github.com/cockroachdb/cockroach/storage"
	"github.com/cockroachdb/cockroach/storage/engine"
	"github.com/cockroachdb/cockroach/testutils"
	"github.com/cockroachdb/cockroach/util/leaktest"
	"github.com/cockroachdb/cockroach/util/log"
)

func adminMergeArgs(key []byte, raftID proto.RaftID, storeID proto.StoreID) proto.AdminMergeRequest {
	return proto.AdminMergeRequest{
		RequestHeader: proto.RequestHeader{
			Key:     key,
			RaftID:  raftID,
			Replica: proto.Replica{StoreID: storeID},
		},
	}
}

func createSplitRanges(store *storage.Store) (*proto.RangeDescriptor, *proto.RangeDescriptor, error) {
	args := adminSplitArgs(proto.KeyMin, []byte("b"), 1, store.StoreID())
	if _, err := store.ExecuteCmd(context.Background(), &args); err != nil {
		return nil, nil, err
	}

	rangeA := store.LookupRange([]byte("a"), nil)
	rangeB := store.LookupRange([]byte("c"), nil)

	if bytes.Equal(rangeA.Desc().StartKey, rangeB.Desc().StartKey) {
		log.Errorf("split ranges keys are equal %q!=%q", rangeA.Desc().StartKey, rangeB.Desc().StartKey)
	}

	return rangeA.Desc(), rangeB.Desc(), nil
}

// TestStoreRangeMergeTwoEmptyRanges tries to merge two empty ranges
// together.
func TestStoreRangeMergeTwoEmptyRanges(t *testing.T) {
	defer leaktest.AfterTest(t)
	store, stopper := createTestStore(t)
	defer stopper.Stop()

	_, _, err := createSplitRanges(store)
	if err != nil {
		t.Fatal(err)
	}

	// Merge the b range back into the a range.
	args := adminMergeArgs(proto.KeyMin, 1, store.StoreID())
	_, err = store.ExecuteCmd(context.Background(), &args)
	if err != nil {
		t.Fatal(err)
	}

	// Verify the merge by looking up keys from both ranges.
	rangeA := store.LookupRange([]byte("a"), nil)
	rangeB := store.LookupRange([]byte("c"), nil)

	if !reflect.DeepEqual(rangeA, rangeB) {
		t.Fatalf("ranges were not merged %+v=%+v", rangeA.Desc(), rangeB.Desc())
	}
}

// TestStoreRangeMergeWithData attempts to merge two collocate ranges
// each containing data.
func TestStoreRangeMergeWithData(t *testing.T) {
	defer leaktest.AfterTest(t)
	content := proto.Key("testing!")

	store, stopper := createTestStore(t)
	defer stopper.Stop()

	aDesc, bDesc, err := createSplitRanges(store)
	if err != nil {
		t.Fatal(err)
	}

	// Write some values left and right of the proposed split key.
	pArgs := putArgs([]byte("aaa"), content, aDesc.RaftID, store.StoreID())
	if _, err := store.ExecuteCmd(context.Background(), &pArgs); err != nil {
		t.Fatal(err)
	}
	pArgs = putArgs([]byte("ccc"), content, bDesc.RaftID, store.StoreID())
	if _, err := store.ExecuteCmd(context.Background(), &pArgs); err != nil {
		t.Fatal(err)
	}

	// Confirm the values are there.
	gArgs := getArgs([]byte("aaa"), aDesc.RaftID, store.StoreID())
	if reply, err := store.ExecuteCmd(context.Background(), &gArgs); err != nil {
		t.Fatal(err)
	} else if gReply := reply.(*proto.GetResponse); !bytes.Equal(gReply.Value.Bytes, content) {
		t.Fatalf("actual value %q did not match expected value %q", gReply.Value.Bytes, content)
	}
	gArgs = getArgs([]byte("ccc"), bDesc.RaftID, store.StoreID())
	if reply, err := store.ExecuteCmd(context.Background(), &gArgs); err != nil {
		t.Fatal(err)
	} else if gReply := reply.(*proto.GetResponse); !bytes.Equal(gReply.Value.Bytes, content) {
		t.Fatalf("actual value %q did not match expected value %q", gReply.Value.Bytes, content)
	}

	// Merge the b range back into the a range.
	args := adminMergeArgs(proto.KeyMin, 1, store.StoreID())
	if _, err := store.ExecuteCmd(context.Background(), &args); err != nil {
		t.Fatal(err)
	}

	// Verify no intents remains on range descriptor keys.
	for _, key := range []proto.Key{keys.RangeDescriptorKey(aDesc.StartKey), keys.RangeDescriptorKey(bDesc.StartKey)} {
		if _, _, err := engine.MVCCGet(store.Engine(), key, store.Clock().Now(), true, nil); err != nil {
			t.Fatal(err)
		}
	}

	// Verify the merge by looking up keys from both ranges.
	rangeA := store.LookupRange([]byte("a"), nil)
	rangeB := store.LookupRange([]byte("c"), nil)

	if !reflect.DeepEqual(rangeA, rangeB) {
		t.Fatalf("ranges were not merged %+v=%+v", rangeA.Desc(), rangeB.Desc())
	}
	if !bytes.Equal(rangeA.Desc().StartKey, proto.KeyMin) {
		t.Fatalf("The start key is not equal to KeyMin %q=%q", rangeA.Desc().StartKey, proto.KeyMin)
	}
	if !bytes.Equal(rangeA.Desc().EndKey, proto.KeyMax) {
		t.Fatalf("The end key is not equal to KeyMax %q=%q", rangeA.Desc().EndKey, proto.KeyMax)
	}

	// Try to get values from after the merge.
	gArgs = getArgs([]byte("aaa"), rangeA.Desc().RaftID, store.StoreID())
	if reply, err := store.ExecuteCmd(context.Background(), &gArgs); err != nil {
		t.Fatal(err)
	} else if gReply := reply.(*proto.GetResponse); !bytes.Equal(gReply.Value.Bytes, content) {
		t.Fatalf("actual value %q did not match expected value %q", gReply.Value.Bytes, content)
	}
	gArgs = getArgs([]byte("ccc"), rangeB.Desc().RaftID, store.StoreID())
	if reply, err := store.ExecuteCmd(context.Background(), &gArgs); err != nil {
		t.Fatal(err)
	} else if gReply := reply.(*proto.GetResponse); !bytes.Equal(gReply.Value.Bytes, content) {
		t.Fatalf("actual value %q did not match expected value %q", gReply.Value.Bytes, content)
	}

	// Put new values after the merge on both sides.
	pArgs = putArgs([]byte("aaaa"), content, rangeA.Desc().RaftID, store.StoreID())
	if _, err = store.ExecuteCmd(context.Background(), &pArgs); err != nil {
		t.Fatal(err)
	}
	pArgs = putArgs([]byte("cccc"), content, rangeB.Desc().RaftID, store.StoreID())
	if _, err = store.ExecuteCmd(context.Background(), &pArgs); err != nil {
		t.Fatal(err)
	}

	// Try to get the newly placed values.
	gArgs = getArgs([]byte("aaaa"), rangeA.Desc().RaftID, store.StoreID())
	if reply, err := store.ExecuteCmd(context.Background(), &gArgs); err != nil {
		t.Fatal(err)
	} else if gReply := reply.(*proto.GetResponse); !bytes.Equal(gReply.Value.Bytes, content) {
		t.Fatalf("actual value %q did not match expected value %q", gReply.Value.Bytes, content)
	}
	gArgs = getArgs([]byte("cccc"), rangeA.Desc().RaftID, store.StoreID())
	if reply, err := store.ExecuteCmd(context.Background(), &gArgs); err != nil {
		t.Fatal(err)
	} else if gReply := reply.(*proto.GetResponse); !bytes.Equal(gReply.Value.Bytes, content) {
		t.Fatalf("actual value %q did not match expected value %q", gReply.Value.Bytes, content)
	}
}

// TestStoreRangeMergeLastRange verifies that merging the last range is a noop.
func TestStoreRangeMergeLastRange(t *testing.T) {
	defer leaktest.AfterTest(t)
	store, stopper := createTestStore(t)
	defer stopper.Stop()

	// Merge last range.
	args := adminMergeArgs(proto.KeyMin, 1, store.StoreID())
	if _, err := store.ExecuteCmd(context.Background(), &args); err != nil {
		t.Fatalf("merge of last range should be a noop: %s", err)
	}
}

// TestStoreRangeMergeNonConsecutive attempts to merge two ranges
// that are not on same store.
func TestStoreRangeMergeNonConsecutive(t *testing.T) {
	defer leaktest.AfterTest(t)
	store, stopper := createTestStore(t)
	defer stopper.Stop()

	// Split into 3 ranges
	argsSplit := adminSplitArgs(proto.KeyMin, []byte("d"), 1, store.StoreID())
	if _, err := store.ExecuteCmd(context.Background(), &argsSplit); err != nil {
		t.Fatalf("Can't split range %s", err)
	}
	argsSplit = adminSplitArgs(proto.KeyMin, []byte("b"), 1, store.StoreID())
	if _, err := store.ExecuteCmd(context.Background(), &argsSplit); err != nil {
		t.Fatalf("Can't split range %s", err)
	}

	rangeA := store.LookupRange([]byte("a"), nil)
	rangeB := store.LookupRange([]byte("c"), nil)
	rangeC := store.LookupRange([]byte("e"), nil)

	if bytes.Equal(rangeA.Desc().StartKey, rangeB.Desc().StartKey) {
		log.Errorf("split ranges keys are equal %q!=%q", rangeA.Desc().StartKey, rangeB.Desc().StartKey)
	}
	if bytes.Equal(rangeB.Desc().StartKey, rangeC.Desc().StartKey) {
		log.Errorf("split ranges keys are equal %q!=%q", rangeB.Desc().StartKey, rangeC.Desc().StartKey)
	}
	if bytes.Equal(rangeA.Desc().StartKey, rangeC.Desc().StartKey) {
		log.Errorf("split ranges keys are equal %q!=%q", rangeA.Desc().StartKey, rangeC.Desc().StartKey)
	}

	// Read all of the system keys touched by the split transactions to
	// resolve intents.  If intents are left unresolved then the
	// asynchronous resolution may happen after the call to RemoveRange
	// below, reviving the range and breaking the test.
	if _, err := store.DB().Scan(keys.LocalMax, keys.SystemMax, 1000); err != nil {
		t.Fatal(err)
	}

	// Remove range B from store and attempt to merge. This is a bit of a hack and leaves some
	// internals in an inconsistent state, so we must re-add the range later.
	// This is sufficient for now to generate the "ranges not collocated" error; if this changes
	// in the future we could make this test more realistic by using a multiTestContext
	// and ChangeReplicas to arrange two ranges onto different stores/nodes.
	//
	// Wait for the leader lease to ensure things are quiescent before removing the range.
	// See #702 and TestStoreExecuteCmdOutOfRange.
	// TODO(bdarnell): refactor this test to rebalance the range onto a separate node
	// when this is supported.
	rangeB.WaitForLeaderLease(t)
	if err := store.RemoveRange(rangeB); err != nil {
		t.Fatal(err)
	}

	argsMerge := adminMergeArgs(rangeA.Desc().StartKey, 1, store.StoreID())
	if _, err := rangeA.AdminMerge(argsMerge); !testutils.IsError(err, "ranges not collocated") {
		t.Fatalf("did not got expected error; got %s", err)
	}

	// Re-add the range. This is necessary for a clean shutdown.
	if err := store.AddRangeTest(rangeB); err != nil {
		t.Fatal(err)
	}
}
