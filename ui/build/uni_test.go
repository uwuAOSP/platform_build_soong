// Copyright (C) 2026 The uwuAOSP Project
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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestUniPrepareDisablesIncrementalAnalysis(t *testing.T) {
	config := Config{&configImpl{incrementalBuildActions: true}}
	config.SetUniPrepareMode()
	if config.incrementalBuildActions {
		t.Fatal("uni prepare must not restore incremental analysis state")
	}
	if !config.incrementalBuildActionsSetInEnv {
		t.Fatal("release configuration may re-enable incremental analysis")
	}
}

func TestReadUniProductPackages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "soong.variables")
	data := `{
  "PartitionVarsForSoongMigrationOnlyDoNotUse": {
    "ProductPackagesSet": {
      "all": {"ProductPackages": ["SystemUI", "framework-res"]}
    }
  }
}`
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	packages, err := readUniProductPackages(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"SystemUI", "framework-res"}; !reflect.DeepEqual(packages, want) {
		t.Fatalf("got %v, want %v", packages, want)
	}
}

func TestStatUniGraphFilesChangesFingerprint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "combined.ninja")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	_, first, err := statUniGraphFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second graph"), 0600); err != nil {
		t.Fatal(err)
	}
	_, second, err := statUniGraphFiles(path)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("graph fingerprint did not change")
	}
}

func TestCollectUniGraphFilesIncludesShards(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "build.product.ninja")
	shard := filepath.Join(directory, "build.product.0.ninja")
	other := filepath.Join(directory, "build.product.variables")
	for path, data := range map[string]string{
		root:  "subninja build.product.0.ninja\n",
		shard: "build output: rule input\n",
		other: "variables",
	} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, first, err := collectUniGraphFiles(root, other)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 3 || files[1].Path != shard {
		t.Fatalf("unexpected graph files: %v", files)
	}
	if err := os.WriteFile(shard, []byte("build changed: rule input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, second, err := collectUniGraphFiles(root, other)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("shard change did not invalidate graph fingerprint")
	}
}

func TestCollectUniGraphFilesIncludesAdditionalFiles(t *testing.T) {
	directory := t.TempDir()
	root := filepath.Join(directory, "build.product.ninja")
	combined := filepath.Join(directory, "combined-product.ninja")
	if err := os.WriteFile(root, []byte("rule noop\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(combined, []byte("subninja build.product.ninja\n"), 0600); err != nil {
		t.Fatal(err)
	}
	files, first, err := collectUniGraphFiles(root, combined)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[1].Path != combined {
		t.Fatalf("unexpected graph files: %v", files)
	}
	if err := os.WriteFile(combined, []byte("subninja changed.ninja\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, second, err := collectUniGraphFiles(root, combined)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("additional graph file change did not invalidate graph fingerprint")
	}
}

func TestUniCombinedNinjaUpdatesR8Pool(t *testing.T) {
	directory := t.TempDir()
	env := Environment([]string{
		"OUT_DIR=" + directory,
		"NINJA_UNI_R8_NUM_JOBS=7",
		"NINJA_UNI_JAVA_NUM_JOBS=11",
		"NINJA_UNI_KOTLIN_NUM_JOBS=6",
	})
	config := Config{&configImpl{
		environ:              &env,
		parallel:             18,
		katiSuffix:           "-test",
		uniNinjaMode:         true,
		uniCombinedNinjaFile: filepath.Join(directory, "uni", "test", "combined.ninja"),
	}}
	ctx := testContext()
	createCombinedBuildNinjaFile(ctx, config)
	data, err := os.ReadFile(config.CombinedNinjaFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pool uni_r8_pool\n depth = 7\n") {
		t.Fatalf("missing R8 pool depth: %s", data)
	}
	if !strings.Contains(string(data), "pool uni_java_pool\n depth = 11\n") {
		t.Fatalf("missing Java pool depth: %s", data)
	}
	if !strings.Contains(string(data), "pool uni_kotlin_pool\n depth = 6\n") {
		t.Fatalf("missing Kotlin pool depth: %s", data)
	}
	env.Set("NINJA_UNI_R8_NUM_JOBS", "9")
	createCombinedBuildNinjaFile(ctx, config)
	data, err = os.ReadFile(config.CombinedNinjaFile())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "pool uni_r8_pool\n depth = 9\n") {
		t.Fatalf("R8 pool was not updated: %s", data)
	}
}
