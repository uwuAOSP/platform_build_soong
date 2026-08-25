// Copyright (C) 2025 The Android Open Source Project
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

package filesystem

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/google/blueprint"
	"github.com/google/blueprint/proptools"

	"android/soong/android"
)

//go:generate go run ../../blueprint/gobtools/codegen

type prebuiltDtboImg struct {
	android.ModuleBase
	properties prebuiltDtboImgProperties
}

type prebuiltDtboImgProperties struct {
	Src                         *string `android:"path"`
	Partition_size              *int64
	Stem                        *string
	Use_avb                     *bool
	Avb_private_key             *string `android:"path"`
	Avb_algorithm               *string
	Avb_rollback_index          *int64
	Avb_rollback_index_location *int64
}

func PrebuiltDtboImgFactory() android.Module {
	module := &prebuiltDtboImg{}
	android.InitAndroidArchModule(module, android.DeviceSupported, android.MultilibFirst)
	module.AddProperties(&module.properties)
	return module
}

// @auto-generate: gob
type DtboImgInfo struct {
	PropFileForMiscInfo android.Path
}

var DtboImgInfoProvider = blueprint.NewProvider[DtboImgInfo]()

func (p *prebuiltDtboImg) GenerateAndroidBuildActions(ctx android.ModuleContext) {
	input := android.PathForModuleSrc(ctx, proptools.String(p.properties.Src))
	output := p.avbAddHash(ctx, input)
	ctx.SetOutputFiles(android.Paths{output}, "")
	android.SetProvider(
		ctx,
		DtboImgInfoProvider,
		DtboImgInfo{
			PropFileForMiscInfo: p.buildPropFileForMiscInfo(ctx),
		},
	)
	info := vbmetaPartitionInfo{
		Name:                  "dtbo",
		Output:                output,
		RollbackIndexLocation: proptools.IntDefault(p.properties.Avb_rollback_index_location, 0),
	}
	if p.properties.Avb_private_key != nil {
		key := android.PathForModuleSrc(ctx, proptools.String(p.properties.Avb_private_key))
		publicKey := android.PathForModuleOut(ctx, "dtbo.avbpubkey")
		ctx.Build(pctx, android.BuildParams{
			Rule:   extractPublicKeyRule,
			Input:  key,
			Output: publicKey,
		})
		info.PublicKey = publicKey
	}
	android.SetProvider(ctx, vbmetaPartitionProvider, info)
}

func (p *prebuiltDtboImg) avbAddHash(ctx android.ModuleContext, input android.Path) android.Path {
	if p.properties.Use_avb != nil && !*p.properties.Use_avb {
		// Do not sign if Use_avb is explicitly turned off.
		return input
	}
	builder := android.NewRuleBuilder(pctx, ctx).SandboxDisabled()
	filename := proptools.StringDefault(p.properties.Stem, input.Base())
	output := android.PathForModuleOut(ctx, filename)
	builder.Command().Text("cp").Input(input).Output(output)
	builder.Command().Text("chmod").FlagWithArg("+w ", output.String())

	cmd := builder.Command().
		BuiltTool("avbtool").
		Text("add_hash_footer").
		FlagWithArg("--image ", output.String()).
		Flag("--partition_name dtbo").
		Textf(`--salt $(sha256sum "%s" "%s" | cut -d " " -f 1 | tr -d '\n')`, ctx.Config().BuildNumberFile(ctx), ctx.Config().Getenv("BUILD_DATETIME_FILE"))

	if p.properties.Partition_size == nil {
		cmd.Flag("--dynamic_partition_size")
	} else {
		cmd.FlagWithArg("--partition_size ", strconv.FormatInt(*p.properties.Partition_size, 10))
	}
	if p.properties.Avb_algorithm != nil {
		cmd.FlagWithArg("--algorithm ", proptools.String(p.properties.Avb_algorithm))
	}
	if p.properties.Avb_private_key != nil {
		cmd.FlagWithInput("--key ", android.PathForModuleSrc(ctx, proptools.String(p.properties.Avb_private_key)))
	}
	if p.properties.Avb_rollback_index != nil {
		cmd.FlagWithArg("--rollback_index ", strconv.FormatInt(*p.properties.Avb_rollback_index, 10))
	}
	if p.properties.Avb_rollback_index_location != nil {
		cmd.FlagWithArg("--rollback_index_location ", strconv.FormatInt(*p.properties.Avb_rollback_index_location, 10))
	}
	fingerprintFile := ctx.Config().BuildFingerprintFile(ctx)
	cmd.FlagWithArg("--prop ", fmt.Sprintf("com.android.build.dtbo.fingerprint:$(cat %s)", fingerprintFile.String())).Implicit(fingerprintFile)

	builder.Build("add_hash_footer", "add_hash_footer")

	return output
}

func (p *prebuiltDtboImg) buildPropFileForMiscInfo(ctx android.ModuleContext) android.Path {
	var sb strings.Builder
	addStr := func(name string, value string) {
		fmt.Fprintf(&sb, "%s=%s\n", name, value)
	}

	addStr("has_dtbo", "true")
	if p.properties.Partition_size != nil {
		addStr("dtbo_size", strconv.FormatInt(*p.properties.Partition_size, 10))
	}
	fingerprintFile := ctx.Config().BuildFingerprintFile(ctx)
	footerArgs := "--prop " + fmt.Sprintf("com.android.build.dtbo.fingerprint:{CONTENTS_OF:%s}", fingerprintFile.String())
	if p.properties.Avb_rollback_index != nil {
		footerArgs += " --rollback_index " + strconv.FormatInt(*p.properties.Avb_rollback_index, 10)
	}
	addStr("avb_dtbo_add_hash_footer_args", footerArgs)
	if p.properties.Avb_algorithm != nil {
		addStr("avb_dtbo_algorithm", proptools.String(p.properties.Avb_algorithm))
	}
	if p.properties.Avb_private_key != nil {
		addStr("avb_dtbo_key_path", proptools.String(p.properties.Avb_private_key))
	}
	if p.properties.Avb_rollback_index_location != nil {
		addStr("avb_dtbo_rollback_index_location", strconv.FormatInt(*p.properties.Avb_rollback_index_location, 10))
	}

	propFilePreProcessing := android.PathForModuleOut(ctx, "prop_for_misc_info_pre_processing")
	android.WriteFileRuleVerbatim(ctx, propFilePreProcessing, sb.String())
	propFile := android.PathForModuleOut(ctx, "prop_file_for_misc_info")
	ctx.Build(pctx, android.BuildParams{
		Rule:     textFileProcessorRule,
		Input:    propFilePreProcessing,
		Output:   propFile,
		Implicit: fingerprintFile,
	})

	return propFile
}
