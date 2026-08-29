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
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSisoUniFastArgsDoNotAffectNativeMake(t *testing.T) {
	if got := sisoUniFastArgs(false); len(got) != 0 {
		t.Fatalf("native make received uni Siso flags: %v", got)
	}
	if got := sisoUniFastArgs(true); len(got) != 3 {
		t.Fatalf("uni Siso flags = %v, want 3 flags", got)
	}
}

func TestPrepareSisoPriorityState(t *testing.T) {
	directory := t.TempDir()
	targets := []string{"kernel", "otapackage"}
	count, err := prepareSisoPriorityState(directory, targets, `["out/kernel","out/kernel"]`, nil, 18)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("priority count = %d, want 1", count)
	}
	data, err := os.ReadFile(filepath.Join(directory, sisoFailedTargetsFile))
	if err != nil {
		t.Fatal(err)
	}
	var state sisoPriorityState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Targets, targets) || !reflect.DeepEqual(state.Failed, []string{"out/kernel"}) {
		t.Fatalf("unexpected priority state: %+v", state)
	}
}

func TestPrepareSisoPriorityStatePreservesRealFailures(t *testing.T) {
	directory := t.TempDir()
	targets := []string{"kernel", "otapackage"}
	previous, err := json.Marshal(sisoPriorityState{Targets: []string{"otapackage", "kernel"}, Failed: []string{"out/failed.o"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, sisoFailedTargetsFile), previous, 0600); err != nil {
		t.Fatal(err)
	}
	count, err := prepareSisoPriorityState(directory, targets, `["out/kernel"]`, nil, 18)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("priority count = %d, want 2", count)
	}
	data, err := os.ReadFile(filepath.Join(directory, sisoFailedTargetsFile))
	if err != nil {
		t.Fatal(err)
	}
	var state sisoPriorityState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if want := []string{"out/failed.o", "out/kernel"}; !reflect.DeepEqual(state.Failed, want) {
		t.Fatalf("priorities = %v, want %v", state.Failed, want)
	}
}

func TestReadWeightedPriorityTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), ninjaWeightListFileName)
	data := "out/medium,1000\nout/slow-b,10000\nout/slow-a,10000\nout/medium,2000\ninvalid\n"
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := readWeightedPriorityTargets(path, 3)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"out/slow-a", "out/slow-b", "out/medium"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("weighted targets = %v, want %v", got, want)
	}
}

func TestPrepareSisoPriorityStateLimitsHintsNotExplicitTargets(t *testing.T) {
	directory := t.TempDir()
	count, err := prepareSisoPriorityState(directory, []string{"otapackage"}, `["out/kernel"]`, []string{"out/r8-a", "out/r8-b"}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("priority count = %d, want 2", count)
	}
	data, err := os.ReadFile(filepath.Join(directory, sisoFailedTargetsFile))
	if err != nil {
		t.Fatal(err)
	}
	var state sisoPriorityState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatal(err)
	}
	if want := []string{"out/kernel", "out/r8-a"}; !reflect.DeepEqual(state.Failed, want) {
		t.Fatalf("priorities = %v, want %v", state.Failed, want)
	}
}
