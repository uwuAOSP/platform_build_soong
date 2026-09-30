/*
 * SPDX-FileCopyrightText: The uwuAOSP Project
 * SPDX-License-Identifier: Apache-2.0
 */

package android

import (
	"reflect"
	"sort"
	"strconv"
	"testing"
)

func TestShardSoongPhonyTargets(t *testing.T) {
	targets := []string{"droid", "droidcore", "launcher", "settings", "systemimage"}
	sort.Strings(targets)
	shards := shardSoongPhonyTargets(targets)
	if len(shards) != soongPhonyNinjaShardCount {
		t.Fatalf("got %d shards, want %d", len(shards), soongPhonyNinjaShardCount)
	}

	seen := make(map[string]bool, len(targets))
	for shardIndex, shard := range shards {
		if !sort.StringsAreSorted(shard) {
			t.Errorf("shard %d is not sorted: %v", shardIndex, shard)
		}
		for _, target := range shard {
			if seen[target] {
				t.Errorf("target %q appears more than once", target)
			}
			seen[target] = true
			if got := soongPhonyNinjaShardIndex(target); got != shardIndex {
				t.Errorf("target %q is in shard %d, want %d", target, shardIndex, got)
			}
		}
	}
	if len(seen) != len(targets) {
		t.Fatalf("got %d distinct targets, want %d", len(seen), len(targets))
	}

	newTarget := "new_module"
	for i := 0; soongPhonyNinjaShardIndex(newTarget) == soongPhonyNinjaShardIndex(targets[0]); i++ {
		newTarget = "new_module_" + strconv.Itoa(i)
	}
	withNewTarget := append(append([]string(nil), targets...), newTarget)
	sort.Strings(withNewTarget)
	newShards := shardSoongPhonyTargets(withNewTarget)
	for _, target := range targets {
		index := soongPhonyNinjaShardIndex(target)
		if !reflect.DeepEqual(shards[index], newShards[index]) {
			t.Errorf("adding an unrelated target changed shard %d: got %v, want %v", index, newShards[index], shards[index])
		}
	}
}
