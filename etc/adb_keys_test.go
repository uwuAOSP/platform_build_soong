// Copyright 2026 The uwuAOSP Project
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package etc

import (
	"testing"

	"android/soong/android"
)

var prepareForAdbKeysTest = android.GroupFixturePreparers(
	android.PrepareForTestWithArchMutator,
	android.FixtureRegisterWithContext(func(ctx android.RegistrationContext) {
		ctx.RegisterModuleType("adb_keys", AdbKeysModuleFactory)
	}),
)

func TestAdbKeysSkipsEmptyProductVariable(t *testing.T) {
	result := prepareForAdbKeysTest.RunTestWithBp(t, `
		adb_keys {
			name: "adb_keys",
			product_specific: true,
		}
	`)

	module := result.Module("adb_keys", "android_arm64_armv8-a").(*AdbKeysModule)
	if !module.IsSkipInstall() {
		t.Fatal("adb_keys should be skipped when PRODUCT_ADB_KEYS is empty")
	}
}
