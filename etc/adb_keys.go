// Copyright 2024 Google Inc. All rights reserved.
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
	"android/soong/android"
)

func init() {
	android.RegisterModuleType("adb_keys", AdbKeysModuleFactory)
}

type AdbKeysModule struct {
	android.ModuleBase
	outputPath  android.Path
	installPath android.InstallPath
}

func AdbKeysModuleFactory() android.Module {
	module := &AdbKeysModule{}
	android.InitAndroidArchModule(module, android.DeviceSupported, android.MultilibFirst)
	return module
}

func (m *AdbKeysModule) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	productVariables := ctx.Config().ProductVariables()

	if !m.ProductSpecific() {
		ctx.ModuleErrorf("adb_keys module type must set product_specific to true")
	}

	adbKeys := android.String(productVariables.AdbKeys)
	if adbKeys == "" {
		m.SkipInstall()
		return
	}

	input := android.ExistentPathForSource(ctx, adbKeys)
	if !input.Valid() {
		ctx.ModuleErrorf("PRODUCT_ADB_KEYS file %q does not exist", adbKeys)
		return
	}

	outputPath := android.PathForModuleOut(ctx, "adb_keys")
	ctx.Build(pctx, android.BuildParams{
		Rule:   android.CpRule,
		Output: outputPath,
		Input:  input.Path(),
	})
	m.installPath = android.PathForModuleInstall(ctx, "etc/security")
	ctx.InstallFile(m.installPath, "adb_keys", outputPath)
	m.outputPath = outputPath
}

func (m *AdbKeysModule) AndroidMkEntries() []android.AndroidMkEntries {
	if m.IsSkipInstall() {
		return []android.AndroidMkEntries{}
	}

	return []android.AndroidMkEntries{
		{
			Class:      "ETC",
			OutputFile: android.OptionalPathForPath(m.outputPath),
		}}
}
