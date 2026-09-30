// Copyright 2026 The Android Open Source Project
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package build

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"android/soong/ui/status"
)

func TestSoongNinjaGenerationMode(t *testing.T) {
	traditional := Config{&configImpl{}}
	if got := soongNinjaGenerationMode(traditional); got != traditionalSoongNinjaMode {
		t.Fatalf("traditional mode = %q, want %q", got, traditionalSoongNinjaMode)
	}
	uni := Config{&configImpl{uniPrepareMode: true}}
	if got := soongNinjaGenerationMode(uni); got != uniSoongNinjaMode {
		t.Fatalf("uni mode = %q, want %q", got, uniSoongNinjaMode)
	}
}

func TestPrimaryBuilderKeepsUniFlagsOutOfTraditionalBuilds(t *testing.T) {
	for _, test := range []struct {
		name                 string
		config               Config
		wantIncrementalFlag  bool
		wantShardedGraphFlag bool
	}{
		{
			name: "traditional build ignores opt-in incremental cache",
			config: Config{&configImpl{
				incrementalBuildActions:         true,
				incrementalBuildActionsSetInEnv: true,
			}},
		},
		{
			name: "uni prepare defaults to fresh analysis and shard output",
			config: Config{&configImpl{
				uniPrepareMode: true,
			}},
			wantShardedGraphFlag: true,
		},
		{
			name: "uni prepare can explicitly opt in to incremental cache",
			config: Config{&configImpl{
				uniPrepareMode:                  true,
				incrementalBuildActions:         true,
				incrementalBuildActionsSetInEnv: true,
			}},
			wantIncrementalFlag:  true,
			wantShardedGraphFlag: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			args := soongAnalysisArgs(test.config)
			gotIncremental, gotSharded := false, false
			for _, arg := range args {
				gotIncremental = gotIncremental || arg == "--incremental-build-actions"
				gotSharded = gotSharded || arg == "--uni-ninja-shards"
			}
			if gotIncremental != test.wantIncrementalFlag || gotSharded != test.wantShardedGraphFlag {
				t.Fatalf("Soong args contain incremental=%t sharded=%t, want incremental=%t sharded=%t: %q",
					gotIncremental, gotSharded, test.wantIncrementalFlag, test.wantShardedGraphFlag, args)
			}
		})
	}
}

func TestRemoveSoongNinjaOutputs(t *testing.T) {
	dir := t.TempDir()
	ninjaFile := filepath.Join(dir, "build.test.ninja")
	files := []string{
		ninjaFile,
		ninjaFile + ".globs",
		filepath.Join(dir, "build.test.0.ninja"),
		filepath.Join(dir, "build.test.0.ninja.blueprint-cache"),
		filepath.Join(dir, "build.test.singleton.0.ninja"),
		filepath.Join(dir, "build.test.singleton.0.ninja.blueprint-cache"),
	}
	for _, file := range files {
		if err := os.WriteFile(file, []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}
	}

	if err := removeSoongNinjaOutputs(ninjaFile); err != nil {
		t.Fatalf("removeSoongNinjaOutputs: %v", err)
	}
	for _, file := range files {
		if _, err := os.Stat(file); !os.IsNotExist(err) {
			t.Errorf("stale graph file %q still exists (stat error: %v)", file, err)
		}
	}
}

func TestPrepareSoongNinjaGenerationModeInvalidatesOnlyOnModeChange(t *testing.T) {
	dir := t.TempDir()
	ninjaFile := filepath.Join(dir, "build.test.ninja")
	if err := os.WriteFile(ninjaFile, []byte("uni graph"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordSoongNinjaGenerationModeFor(ninjaFile, uniSoongNinjaMode); err != nil {
		t.Fatal(err)
	}

	if err := prepareSoongNinjaGenerationModeFor(ninjaFile, traditionalSoongNinjaMode); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ninjaFile); !os.IsNotExist(err) {
		t.Fatalf("mode change did not invalidate graph, stat error: %v", err)
	}

	if err := os.WriteFile(ninjaFile, []byte("traditional graph"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := recordSoongNinjaGenerationModeFor(ninjaFile, traditionalSoongNinjaMode); err != nil {
		t.Fatal(err)
	}
	if err := prepareSoongNinjaGenerationModeFor(ninjaFile, traditionalSoongNinjaMode); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ninjaFile); err != nil {
		t.Fatalf("same mode unexpectedly invalidated graph: %v", err)
	}
}

func TestUniStatusForwarderStreamsNinjaActions(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "status.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	base := &recordingToolStatus{}
	statusValue := newUniStatusForwarder(base, listener.Addr().String())
	forwarder, ok := statusValue.(*uniStatusForwarder)
	if !ok {
		t.Fatal("status forwarder did not connect to the Uni socket")
	}
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	estimate := time.Now().Add(5 * time.Minute)
	action := &status.Action{Description: "compile example module"}
	statusValue.SetTotalActions(23)
	statusValue.SetEstimatedTime(estimate)
	statusValue.StartAction(action)
	statusValue.FinishAction(status.ActionResult{Action: action})
	forwarder.Close()

	decoder := json.NewDecoder(connection)
	events := make([]uniStatusEvent, 0, 5)
	for range 5 {
		var event uniStatusEvent
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode status event %d: %v", len(events), err)
		}
		events = append(events, event)
	}
	if events[0].Type != "reset" ||
		events[1].Type != "total" || events[1].Total != 23 ||
		events[2].Type != "estimate" || events[2].EstimatedTimeUnixNano != estimate.UnixNano() ||
		events[3].Type != "start" || events[3].ID == 0 || events[3].Description != action.Description ||
		events[4].Type != "finish" || events[4].ID != events[3].ID || events[4].Description != action.Description {
		t.Fatalf("unexpected forwarded events: %+v", events)
	}
	if base.total != 23 || len(base.started) != 1 || len(base.finished) != 1 {
		t.Fatalf("forwarding did not preserve normal status callbacks: %+v", base)
	}
}

func TestUniStatusForwarderStreamsKatiActions(t *testing.T) {
	listener, err := net.Listen("unix", filepath.Join(t.TempDir(), "status.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	base := &recordingToolStatus{}
	statusValue := newUniStatusForwarder(base, listener.Addr().String())
	forwarder, ok := statusValue.(*uniStatusForwarder)
	if !ok {
		t.Fatal("status forwarder did not connect to the Uni socket")
	}
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer connection.Close()

	status.KatiReader(statusValue, io.NopCloser(strings.NewReader("[1/2] including build/make/core/config.mk\n")))
	forwarder.Close()

	decoder := json.NewDecoder(connection)
	events := make([]uniStatusEvent, 0, 4)
	for range 4 {
		var event uniStatusEvent
		if err := decoder.Decode(&event); err != nil {
			t.Fatalf("decode Kati status event %d: %v", len(events), err)
		}
		events = append(events, event)
	}
	if events[0].Type != "reset" || events[1].Type != "total" || events[1].Total != 2 ||
		events[2].Type != "start" || events[2].Description != "including build/make/core/config.mk" ||
		events[3].Type != "finish" || events[3].ID != events[2].ID || events[3].Description != events[2].Description {
		t.Fatalf("unexpected Kati events: %+v", events)
	}
	if len(base.started) != 1 || len(base.finished) != 1 {
		t.Fatalf("Kati callbacks were not preserved: %+v", base)
	}
}

type recordingToolStatus struct {
	total    int
	started  []*status.Action
	finished []status.ActionResult
}

func (r *recordingToolStatus) SetTotalActions(total int)  { r.total = total }
func (r *recordingToolStatus) SetEstimatedTime(time.Time) {}
func (r *recordingToolStatus) StartAction(action *status.Action) {
	r.started = append(r.started, action)
}
func (r *recordingToolStatus) FinishAction(result status.ActionResult) {
	r.finished = append(r.finished, result)
}
func (r *recordingToolStatus) Verbose(string) {}
func (r *recordingToolStatus) Status(string)  {}
func (r *recordingToolStatus) Print(string)   {}
func (r *recordingToolStatus) Error(string)   {}
func (r *recordingToolStatus) Finish()        {}

func TestSoongBootstrapStatusReplacesPrimaryBuilderAction(t *testing.T) {
	recording := &recordingToolStatus{}
	tool := newSoongBootstrapStatus(recording, "out/soong/build.product.ninja")
	tool.SetTotalActions(3)

	primary := &status.Action{
		Description: "analyzing Android.bp files",
		Outputs:     []string{"out/soong/build.product.ninja"},
	}
	bootstrap := &status.Action{Description: "compile soong_build"}
	tool.StartAction(primary)
	tool.StartAction(bootstrap)
	tool.FinishAction(status.ActionResult{Action: primary})
	tool.FinishAction(status.ActionResult{Action: bootstrap})

	if recording.total != 2 {
		t.Fatalf("expected primary builder to be removed from total, got %d", recording.total)
	}
	if len(recording.started) != 1 || recording.started[0] != bootstrap {
		t.Fatalf("unexpected visible actions: %#v", recording.started)
	}
	if len(recording.finished) != 1 || recording.finished[0].Action != bootstrap {
		t.Fatalf("unexpected finished actions: %#v", recording.finished)
	}
}

func TestSoongBootstrapStatusRestoresFailedPrimaryBuilder(t *testing.T) {
	recording := &recordingToolStatus{}
	tool := newSoongBootstrapStatus(recording, "out/soong/build.product.ninja")
	primary := &status.Action{Outputs: []string{"out/soong/build.product.ninja"}}
	tool.StartAction(primary)
	tool.FinishAction(status.ActionResult{Action: primary, Error: errors.New("failed")})

	if len(recording.started) != 1 || recording.started[0] != primary {
		t.Fatalf("failed primary builder was not restored: %#v", recording.started)
	}
	if len(recording.finished) != 1 || recording.finished[0].Error == nil {
		t.Fatalf("failed primary builder result was not reported: %#v", recording.finished)
	}
}
