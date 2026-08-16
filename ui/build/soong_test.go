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
	"errors"
	"testing"
	"time"

	"android/soong/ui/status"
)

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
