// Copyright 2021 Google Inc. All rights reserved.
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

package kernel

import (
	"fmt"
	"os"
	"path"
	"testing"

	"android/soong/android"
	"android/soong/cc"
)

func TestKernelModulesDlkmPartitionTags(t *testing.T) {
	for _, partition := range []string{"system_dlkm", "vendor_dlkm", "odm_dlkm"} {
		t.Run(partition, func(t *testing.T) {
			result := android.GroupFixturePreparers(
				cc.PrepareForTestWithCcDefaultModules,
				android.PrepareForTestWithHostTools("kernel_modules_builder", "zipsync", "soong_zip", "merge_zips", "depmod"),
				android.FixtureRegisterWithContext(registerKernelBuildComponents),
				android.FixtureAddFile("module.ko", nil),
			).RunTestWithBp(t, fmt.Sprintf(`
prebuilt_kernel_modules {
	name: "modules",
	srcs: ["module.ko"],
	%s_specific: true,
}
`, partition))
			module := result.ModuleForTests(t, "modules", "android_arm64_armv8-a").Module()
			info := android.OtherModulePointerProviderOrDefault(result, module, android.CommonModuleInfoProvider)
			android.AssertStringEquals(t, "fsgen must collect the DLKM installer in its owning partition", partition, info.PartitionTag)
			specs := android.GetInstallFiles(result, module).PackagingSpecs
			if len(specs) == 0 {
				t.Fatal("kernel installer must package modules.load and its extra module archive")
			}
			for _, spec := range specs {
				android.AssertStringEquals(t, "partition tag must agree with packaged kernel files", info.PartitionTag, spec.Partition())
				if !spec.ExtraZip().Valid() {
					t.Error("kernel modules archive must accompany modules.load")
				}
			}
		})
	}
}

func TestKernelModulesFilelist(t *testing.T) {
	ctx := android.GroupFixturePreparers(
		cc.PrepareForTestWithCcDefaultModules,
		android.PrepareForTestWithHostTools("kernel_modules_builder", "zipsync", "soong_zip", "merge_zips", "depmod"),
		android.FixtureRegisterWithContext(registerKernelBuildComponents),
		android.MockFS{
			"depmod.cpp": nil,
			"mod1.ko":    nil,
			"mod2.ko":    nil,
		}.AddToFixture(),
	).RunTestWithBp(t, `
		prebuilt_kernel_modules {
			name: "foo",
			srcs: ["*.ko"],
			kernel_version: "5.10",
		}
	`)

	expected := []string{
		"lib/modules/5.10/modules.load",
	}
	expectedZips := []string{
		"installs.zip unzips to lib/modules/5.10",
	}

	var actual []string
	var actualZips []string
	for _, ps := range android.GetInstallFiles(
		ctx, ctx.ModuleForTests(t, "foo", "android_arm64_armv8-a").Module()).PackagingSpecs {
		actual = append(actual, ps.RelPathInPackage())
		zip := ps.ExtraZip()
		if zip.Valid() {
			actualZips = append(actualZips,
				path.Base(zip.String())+" unzips to "+path.Dir(ps.RelPathInPackage()))
		}
	}
	actual = android.SortedUniqueStrings(actual)
	expected = android.SortedUniqueStrings(expected)
	android.AssertDeepEquals(t, "foo packaging specs", expected, actual)
	actualZips = android.SortedUniqueStrings(actualZips)
	expectedZips = android.SortedUniqueStrings(expectedZips)
	android.AssertDeepEquals(t, "foo extra zips", expectedZips, actualZips)
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
