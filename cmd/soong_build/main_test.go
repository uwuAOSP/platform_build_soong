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

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalysisProgress(t *testing.T) {
	tempDir := t.TempDir()
	oldTop := topDir
	topDir = tempDir
	defer func() { topDir = oldTop }()

	moduleList := filepath.Join("out", "Android.bp.list")
	if err := os.MkdirAll(filepath.Join(tempDir, "out"), 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, moduleList), []byte("Android.bp\npackages/apps/Settings/Android.bp\n"), 0666); err != nil {
		t.Fatal(err)
	}

	statusFile := filepath.Join(tempDir, "out", "analysis.status")
	progress := newAnalysisProgress(statusFile, moduleList)
	progress.eventStarted("parse_bp")
	progress.eventProgress("ninja_buckets", 0, 512)
	progress.eventProgress("ninja_bucket_start", 1, 512)
	progress.eventProgress("ninja_bucket_finish", 1, 512)

	data, err := os.ReadFile(statusFile)
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{
		"stage\tParsing 2 Android.bp files...",
		"progress\tninja_buckets\t0\t512",
		"progress\tninja_bucket_start\t1\t512",
		"progress\tninja_bucket_finish\t1\t512",
	}, "\n")
	if got := strings.TrimSpace(string(data)); got != want {
		t.Fatalf("unexpected analysis status: got %q, want %q", got, want)
	}
}

func TestAnalysisProgressIgnoresInternalEvents(t *testing.T) {
	statusFile := filepath.Join(t.TempDir(), "analysis.status")
	progress := newAnalysisProgress(statusFile, "missing.list")
	progress.eventStarted("internal_event")
	if _, err := os.Stat(statusFile); !os.IsNotExist(err) {
		t.Fatalf("internal event unexpectedly changed user-visible status: %v", err)
	}
}
