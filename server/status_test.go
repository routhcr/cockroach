// Copyright 2014 The Cockroach Authors.
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
// Author: Spencer Kimball (spencer.kimball@gmail.com)
// Author: Bram Gruneir (bram+code@cockroachlabs.com)

package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"reflect"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/cockroach/proto"
	"github.com/cockroachdb/cockroach/server/status"
	"github.com/cockroachdb/cockroach/storage"
	"github.com/cockroachdb/cockroach/ts"
	"github.com/cockroachdb/cockroach/util"
	"github.com/cockroachdb/cockroach/util/leaktest"
	"github.com/cockroachdb/cockroach/util/log"
)

// TestStatusLocalStacks verifies that goroutine stack traces are available
// via the /_status/stacks/local endpoint.
func TestStatusLocalStacks(t *testing.T) {
	defer leaktest.AfterTest(t)
	s := StartTestServer(t)
	defer s.Stop()

	body, err := getText(testContext.RequestScheme() + "://" + s.ServingAddr() + "/_status/stacks/local")
	if err != nil {
		t.Fatal(err)
	}
	// Verify match with at least two goroutine stacks.
	re := regexp.MustCompile("(?s)goroutine [0-9]+.*goroutine [0-9]+.*")
	if !re.Match(body) {
		t.Errorf("expected %s to match %s", body, re)
	}

	body, err = getText(testContext.RequestScheme() + "://" + s.ServingAddr() + "/_status/stacks/1")
	if err != nil {
		t.Fatal(err)
	}
	// Verify match with at least two goroutine stacks.
	if !re.Match(body) {
		t.Errorf("expected %s to match %s", body, re)
	}
}

// TestStatusJson verifies that status endpoints return expected Json results.
// The content type of the responses is always util.JSONContentType.
func TestStatusJson(t *testing.T) {
	defer leaktest.AfterTest(t)
	s := StartTestServer(t)
	defer s.Stop()

	type TestCase struct {
		keyPrefix string
		expected  string
	}

	addr, err := s.Gossip().GetNodeIDAddress(s.Gossip().GetNodeID())
	if err != nil {
		t.Fatal(err)
	}
	testCases := []TestCase{
		{statusNodesPrefix, "\\\"d\\\":"},
	}
	expectedResult := fmt.Sprintf(`{
  "nodeID": 1,
  "address": {
    "network": "%s",
    "string": "%s"
  },
  "buildInfo": {
    "goVersion": "%s",
    "tag": "",
    "time": "",
    "dependencies": ""
  }
}`, addr.Network(), addr.String(), regexp.QuoteMeta(runtime.Version()))
	testCases = append(testCases, TestCase{"/_status/details/local", expectedResult})
	testCases = append(testCases, TestCase{"/_status/details/1", expectedResult})

	httpClient, err := testContext.GetHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range testCases {
		contentTypes := []string{util.JSONContentType, util.ProtoContentType, util.YAMLContentType}
		for _, contentType := range contentTypes {
			req, err := http.NewRequest("GET", testContext.RequestScheme()+"://"+s.ServingAddr()+spec.keyPrefix, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set(util.AcceptHeader, contentType)
			resp, err := httpClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Errorf("unexpected status code: %v", resp.StatusCode)
			}
			returnedContentType := resp.Header.Get(util.ContentTypeHeader)
			if returnedContentType != util.JSONContentType {
				t.Errorf("unexpected content type: %v", returnedContentType)
			}
			body, err := ioutil.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if re := regexp.MustCompile(spec.expected); !re.Match(body) {
				t.Errorf("expected match %s; got %s", spec.expected, body)
			}
		}
	}
}

// TestStatusGossipJson ensures that the output response for the full gossip
// info contains the required fields.
func TestStatusGossipJson(t *testing.T) {
	defer leaktest.AfterTest(t)
	s := StartTestServer(t)
	defer s.Stop()

	type prefixedInfo struct {
		Key string                 `json:"Key"`
		Val []storage.PrefixConfig `json:"Val"`
	}

	type rangeDescriptorInfo struct {
		Key string                `json:"Key"`
		Val proto.RangeDescriptor `json:"Val"`
	}

	type keyValueStringPair struct {
		Key string `json:"Key"`
		Val string `json:"Val"`
	}

	type keyValuePair struct {
		Key string                 `json:"Key"`
		Val map[string]interface{} `json:"Val"`
	}

	type infos struct {
		Infos struct {
			Accounting  *prefixedInfo        `json:"accounting"`
			FirstRange  *rangeDescriptorInfo `json:"first-range"`
			Permissions *prefixedInfo        `json:"permissions"`
			Zones       *prefixedInfo        `json:"zones"`
			ClusterID   *keyValueStringPair  `json:"cluster-id"`
			Node1       *keyValuePair        `json:"node:1"`
		} `json:"infos"`
	}

	httpClient, err := testContext.GetHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	contentTypes := []string{util.JSONContentType, util.ProtoContentType, util.YAMLContentType}
	for _, contentType := range contentTypes {
		req, err := http.NewRequest("GET", testContext.RequestScheme()+"://"+s.ServingAddr()+"/_status/gossip/local", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(util.AcceptHeader, contentType)

		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("unexpected status code: %v", resp.StatusCode)
		}
		returnedContentType := resp.Header.Get(util.ContentTypeHeader)
		if returnedContentType != util.JSONContentType {
			t.Errorf("unexpected content type: %v", returnedContentType)
		}
		body, err := ioutil.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		data := &infos{}
		if err = json.Unmarshal(body, &data); err != nil {
			t.Fatal(err)
		}
		if data.Infos.Accounting == nil {
			t.Errorf("no accounting info returned: %v,", body)
		}
		if data.Infos.FirstRange == nil {
			t.Errorf("no first-range info returned: %v", body)
		}
		if data.Infos.Permissions == nil {
			t.Errorf("no permission info returned: %v", body)
		}
		if data.Infos.Zones == nil {
			t.Errorf("no zone info returned: %v", body)
		}
		if data.Infos.ClusterID == nil {
			t.Errorf("no clusterID info returned: %v", body)
		}
		if data.Infos.Node1 == nil {
			t.Errorf("no node 1 info returned: %v", body)
		}
	}
}

// getRequest returns the the results of a get request to the test server with
// the given path.  It returns the contents of the body of the result.
func getRequest(t *testing.T, ts TestServer, path string) []byte {
	httpClient, err := testContext.GetHTTPClient()
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest("GET", testContext.RequestScheme()+"://"+ts.ServingAddr()+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(util.AcceptHeader, util.JSONContentType)
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("unexpected status code: %v", resp.StatusCode)
	}
	returnedContentType := resp.Header.Get(util.ContentTypeHeader)
	if returnedContentType != util.JSONContentType {
		t.Errorf("unexpected content type: %v", returnedContentType)
	}
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// startServer will start a server with a short scan interval, wait for
// the scan to complete, and return the server. The caller is
// responsible for stopping the server.
// TODO(Bram): Add more nodes.
func startServer(t *testing.T, keyPrefix string) TestServer {
	var ts TestServer
	ts.Ctx = NewTestContext()
	ts.Ctx.ScanInterval = time.Duration(5 * time.Millisecond)
	ts.StoresPerNode = 3
	if err := ts.Start(); err != nil {
		t.Fatal(err)
	}

	// Make sure the range is spun up with an arbitrary read command. We do not
	// expect a specific response.
	if _, err := ts.db.Get("a"); err != nil {
		t.Fatal(err)
	}

	// Make sure the node status is available. This is done by forcing stores to
	// publish their status, synchronizing to the event feed with a canary
	// event, and then forcing the server to write summaries immediately.
	if err := ts.node.publishStoreStatuses(); err != nil {
		t.Fatalf("error publishing store statuses: %s", err)
	}
	syncEvent := status.NewTestSyncEvent(1)
	ts.EventFeed().Publish(syncEvent)
	if err := syncEvent.Sync(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	if err := ts.writeSummaries(); err != nil {
		t.Fatalf("error writing summaries: %s", err)
	}

	return ts
}

// TestStatusLocalLogs checks to ensure that local/logfiles,
// local/logfiles/{filename}, local/log and local/log/{level} function
// correctly.
func TestStatusLocalLogs(t *testing.T) {
	defer leaktest.AfterTest(t)
	dir, err := ioutil.TempDir("", "local_log_test")
	if err != nil {
		t.Fatal(err)
	}
	log.EnableLogFileOutput(dir)
	defer func() {
		log.DisableLogFileOutput()
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
	}()

	ts := startServer(t, "/_status/logfiles/local")
	defer ts.Stop()

	// Log an error which we expect to show up on every log file.
	timestamp := time.Now().UnixNano()
	log.Errorf("TestStatusLocalLogFile test message-Error")
	timestampE := time.Now().UnixNano()
	log.Warningf("TestStatusLocalLogFile test message-Warning")
	timestampEW := time.Now().UnixNano()
	log.Infof("TestStatusLocalLogFile test message-Info")
	timestampEWI := time.Now().UnixNano()

	type logsWrapper struct {
		Data []log.FileInfo `json:"d"`
	}
	var logs logsWrapper
	if err := json.Unmarshal(getRequest(t, ts, "/_status/logfiles/local"), &logs); err != nil {
		t.Fatal(err)
	}
	if a, e := len(logs.Data), 3; a != e {
		t.Fatalf("expected %d log files; got %d", e, a)
	}
	for i, name := range []string{"log.ERROR", "log.INFO", "log.WARNING"} {
		if !strings.Contains(logs.Data[i].Name, name) {
			t.Errorf("expected log file name %s to contain %q", logs.Data[i].Name, name)
		}
	}

	// Fetch the full list of log entries.
	type logWrapper struct {
		Data []log.LogEntry `json:"d"`
	}

	// Check each individual log can be fetched and is non-empty.
	var foundInfo, foundWarning, foundError bool
	for _, file := range logs.Data {
		var log logWrapper
		if err := json.Unmarshal(getRequest(t, ts, fmt.Sprintf("/_status/logfiles/local/%s", file.Name)), &log); err != nil {
			t.Fatal(err)
		}
		for _, entry := range log.Data {
			switch entry.Format {
			case "TestStatusLocalLogFile test message-Error":
				foundError = true
			case "TestStatusLocalLogFile test message-Warning":
				foundWarning = true
			case "TestStatusLocalLogFile test message-Info":
				foundInfo = true
			}
		}
	}

	if !(foundInfo && foundWarning && foundError) {
		t.Errorf("expected to find test messages in %v", logs.Data)
	}

	testCases := []struct {
		Level           log.Severity
		MaxEntities     int
		StartTimestamp  int64
		EndTimestamp    int64
		Pattern         string
		ExpectedError   bool
		ExpectedWarning bool
		ExpectedInfo    bool
	}{
		// Test filtering by log severity.
		{log.InfoLog, 0, 0, 0, "", true, true, true},
		{log.WarningLog, 0, 0, 0, "", true, true, false},
		{log.ErrorLog, 0, 0, 0, "", true, false, false},
		// Test entry limit. Ignore Info/Warning/Error filters.
		{log.InfoLog, 1, timestamp, timestampEWI, "", false, false, false},
		{log.InfoLog, 2, timestamp, timestampEWI, "", false, false, false},
		{log.InfoLog, 3, timestamp, timestampEWI, "", false, false, false},
		// Test filtering in different timestamp windows.
		{log.InfoLog, 0, timestamp, timestamp, "", false, false, false},
		{log.InfoLog, 0, timestamp, timestampE, "", true, false, false},
		{log.InfoLog, 0, timestampE, timestampEW, "", false, true, false},
		{log.InfoLog, 0, timestampEW, timestampEWI, "", false, false, true},
		{log.InfoLog, 0, timestamp, timestampEW, "", true, true, false},
		{log.InfoLog, 0, timestampE, timestampEWI, "", false, true, true},
		{log.InfoLog, 0, timestamp, timestampEWI, "", true, true, true},
		// Test filtering by regexp pattern.
		{log.InfoLog, 0, 0, 0, "Info", false, false, true},
		{log.InfoLog, 0, 0, 0, "Warning", false, true, false},
		{log.InfoLog, 0, 0, 0, "Error", true, false, false},
		{log.InfoLog, 0, 0, 0, "Info|Error|Warning", true, true, true},
		{log.InfoLog, 0, 0, 0, "Nothing", false, false, false},
	}

	for i, testCase := range testCases {
		var url bytes.Buffer
		fmt.Fprintf(&url, "/_status/logs/local?level=%s", testCase.Level.Name())
		if testCase.MaxEntities > 0 {
			fmt.Fprintf(&url, "&max=%d", testCase.MaxEntities)
		}
		if testCase.StartTimestamp > 0 {
			fmt.Fprintf(&url, "&starttime=%d", testCase.StartTimestamp)
		}
		if testCase.StartTimestamp > 0 {
			fmt.Fprintf(&url, "&endtime=%d", testCase.EndTimestamp)
		}
		if len(testCase.Pattern) > 0 {
			fmt.Fprintf(&url, "&pattern=%s", testCase.Pattern)
		}

		var log logWrapper
		path := url.String()
		if err := json.Unmarshal(getRequest(t, ts, path), &log); err != nil {
			t.Fatal(err)
		}

		if testCase.MaxEntities > 0 {
			if a, e := len(log.Data), testCase.MaxEntities; a != e {
				t.Errorf("%d expected %d entries, got %d: \n%+v", i, e, a, log.Data)
			}
		} else {
			var actualInfo, actualWarning, actualError bool
			var formats bytes.Buffer
			for _, entry := range log.Data {
				fmt.Fprintf(&formats, "%s\n", entry.Format)

				switch entry.Format {
				case "TestStatusLocalLogFile test message-Error":
					actualError = true
				case "TestStatusLocalLogFile test message-Warning":
					actualWarning = true
				case "TestStatusLocalLogFile test message-Info":
					actualInfo = true
				}
			}

			if !(testCase.ExpectedInfo == actualInfo &&
				testCase.ExpectedWarning == actualWarning &&
				testCase.ExpectedError == actualError) {

				t.Errorf(
					"%d: expected info, warning, error: (%t, %t, %t) from %s, got:\n%s",
					i,
					testCase.ExpectedInfo,
					testCase.ExpectedWarning,
					testCase.ExpectedError,
					path,
					formats.String(),
				)
			}
		}
	}
}

// TestNodeStatusResponse verifies that node status returns the expected
// results.
func TestNodeStatusResponse(t *testing.T) {
	defer leaktest.AfterTest(t)
	ts := startServer(t, statusNodesPrefix)
	defer ts.Stop()

	body := getRequest(t, ts, statusNodesPrefix)

	// First fetch all the node statuses.
	type nsWrapper struct {
		Data []status.NodeStatus `json:"d"`
	}
	wrapper := nsWrapper{}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		t.Fatal(err)
	}
	nodeStatuses := wrapper.Data

	if len(nodeStatuses) != 1 {
		t.Errorf("too many node statuses returned - expected:1 actual:%d", len(nodeStatuses))
	}
	if !reflect.DeepEqual(ts.node.Descriptor, nodeStatuses[0].Desc) {
		t.Errorf("node status descriptors are not equal\nexpected:%+v\nactual:%+v\n", ts.node.Descriptor, nodeStatuses[0].Desc)
	}

	// Now fetch each one individually. Loop through the nodeStatuses to use the
	// ids only.
	for _, oldNodeStatus := range nodeStatuses {
		nodeStatus := &status.NodeStatus{}
		requestBody := getRequest(t, ts, fmt.Sprintf("%s%s", statusNodesPrefix, oldNodeStatus.Desc.NodeID))
		if err := json.Unmarshal(requestBody, &nodeStatus); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ts.node.Descriptor, nodeStatus.Desc) {
			t.Errorf("node status descriptors are not equal\nexpected:%+v\nactual:%+v\n", ts.node.Descriptor, nodeStatus.Desc)
		}
	}
}

// TestStoreStatusResponse verifies that node status returns the expected
// results.
func TestStoreStatusResponse(t *testing.T) {
	defer leaktest.AfterTest(t)
	ts := startServer(t, statusStoresPrefix)
	defer ts.Stop()

	body := getRequest(t, ts, statusStoresPrefix)

	type ssWrapper struct {
		Data []storage.StoreStatus `json:"d"`
	}
	wrapper := ssWrapper{}
	if err := json.Unmarshal(body, &wrapper); err != nil {
		t.Fatal(err)
	}
	storeStatuses := wrapper.Data

	if len(storeStatuses) != ts.node.lSender.GetStoreCount() {
		t.Errorf("expected %d node statuses, got %d", ts.node.lSender.GetStoreCount(), len(storeStatuses))
	}
	for _, storeStatus := range storeStatuses {
		storeID := storeStatus.Desc.StoreID
		store, err := ts.node.lSender.GetStore(storeID)
		if err != nil {
			t.Fatal(err)
		}
		desc, err := store.Descriptor()
		if err != nil {
			t.Fatal(err)
		}
		// The capacities fluctuate a lot, so drop them for the deep equal.
		desc.Capacity = proto.StoreCapacity{}
		storeStatus.Desc.Capacity = proto.StoreCapacity{}
		if !reflect.DeepEqual(*desc, storeStatus.Desc) {
			t.Errorf("store status descriptors are not equal\nexpected:%+v\nactual:%+v\n", *desc, storeStatus.Desc)
		}

		// Also fetch the each status individually.
		fetchedStoreStatus := &storage.StoreStatus{}
		requestBody := getRequest(t, ts, fmt.Sprintf("%s%s", statusStoresPrefix, storeStatus.Desc.StoreID))
		if err := json.Unmarshal(requestBody, &fetchedStoreStatus); err != nil {
			t.Fatal(err)
		}
		fetchedStoreStatus.Desc.Capacity = proto.StoreCapacity{}
		if !reflect.DeepEqual(*desc, fetchedStoreStatus.Desc) {
			t.Errorf("store status descriptors are not equal\nexpected:%+v\nactual:%+v\n", *desc, fetchedStoreStatus.Desc)
		}
	}
}

// TestMetricsRecording verifies that Node statistics are periodically recorded
// as time series data.
func TestMetricsRecording(t *testing.T) {
	defer leaktest.AfterTest(t)
	tsrv := &TestServer{}
	tsrv.Ctx = NewTestContext()
	tsrv.Ctx.MetricsFrequency = 5 * time.Millisecond
	if err := tsrv.Start(); err != nil {
		t.Fatal(err)
	}
	defer tsrv.Stop()

	checkTimeSeriesKey := func(now int64, keyName string) error {
		key := ts.MakeDataKey(keyName, "", ts.Resolution10s, now)
		data := &proto.InternalTimeSeriesData{}
		return tsrv.db.GetProto(key, data)
	}

	// Verify that metrics for the current timestamp are recorded. This should
	// be true very quickly.
	util.SucceedsWithin(t, time.Second, func() error {
		now := tsrv.Clock().PhysicalNow()
		if err := checkTimeSeriesKey(now, "cr.store.livebytes.1"); err != nil {
			return err
		}
		if err := checkTimeSeriesKey(now, "cr.node.sys.allocbytes.1"); err != nil {
			return err
		}
		return nil
	})
}
