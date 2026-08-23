// Copyright 2019 Google Inc. All rights reserved.
// Copyright (C) 2026 The uwuAOSP Project
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

package java

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"path/filepath"
	"strings"

	"android/soong/android"

	"github.com/google/blueprint"
)

type KotlinCompileData struct {
	pcStateFileNew   android.OutputPath
	pcStateFilePrior android.OutputPath
	diffFile         android.OutputPath
}

const inputDeltaCmd = `${config.FindInputDeltaCmd} ` +
	`--target "$out" ` +
	`--inputs_file "$out.rsp" ` +
	`--new_state "$newStateFile" ` +
	`--prior_state "$priorStateFile" ` +
	`--inspect $srcJars ` +
	`--version 2 ` +
	`> $sourceDeltaFile`

const kotlinZipSyncCmd = `mkdir -p $srcJarDir && ` +
	`${config.ZipSyncCmd} -d $srcJarDir -l $srcJarDir/list -f "*.java" -f "*.kt" $srcJars`

const nonIncKotlinCmd = `rm -rf "$classesDir" "$headerClassesDir" "$srcJarDir" "$kotlinBuildFile" "$emptyDir" && ` +
	`mkdir -p "$classesDir" "$headerClassesDir" "$emptyDir" && ` +
	kotlinZipSyncCmd + ` && ` +
	`${config.GenKotlinBuildFileCmd} --classpath "$classpath" $friendPathsArg --name "$name"` +
	` --out_dir "$classesDir" --srcs "$out.rsp" --srcs "$srcJarDir/list"` +
	` $commonSrcFilesArg --out "$kotlinBuildFile" && ` +
	`${config.KotlincCmd} ${config.KotlincGlobalFlags} ` +
	` ${config.KotlincSuppressJDK9Warnings} ${config.KotlincHeapFlags} ` +
	` $kotlincFlags -jvm-target $kotlinJvmTarget $composePluginFlag $kotlincPluginFlags -Xbuild-file=$kotlinBuildFile ` +
	` -kotlin-home $emptyDir ` +
	` -Xplugin=${config.KotlinAbiGenPluginJar} ` +
	` -P plugin:org.jetbrains.kotlin.jvm.abi:outputDir=$headerClassesDir && ` +
	`${config.SoongZipCmd} -jar $jarArgs -o $out -C $classesDir -D $classesDir -write_if_changed && ` +
	`${config.SoongZipCmd} -jar $jarArgs -o $headerJar -C $headerClassesDir -D $headerClassesDir -write_if_changed && ` +
	`rm -rf "$srcJarDir" "$classesDir" "$headerClassesDir" `

const moveDeltaStateFile = `mv $newStateFile $priorStateFile && rm $sourceDeltaFile`

var kotlinc = pctx.AndroidRemoteStaticRule("kotlinc", android.RemoteRuleSupports{},
	blueprint.RuleParams{
		Pool:    android.UniKotlinPool,
		Command: inputDeltaCmd + ` && ` + nonIncKotlinCmd + ` && ` + moveDeltaStateFile,
		CommandDeps: []string{
			"${config.FindInputDeltaCmd}",
			"${config.KotlincCmd}",
			"${config.KotlinCompilerJar}",
			"${config.KotlinPreloaderJar}",
			"${config.KotlinReflectJar}",
			"${config.KotlinScriptRuntimeJar}",
			"${config.KotlinStdlibJar}",
			"${config.KotlinTrove4jJar}",
			"${config.KotlinAnnotationJar}",
			"${config.KotlinAbiGenPluginJar}",
			"${config.GenKotlinBuildFileCmd}",
			"${config.SoongZipCmd}",
			"${config.ZipSyncCmd}",
		},
		Rspfile:         "$out.rsp",
		RspfileContent:  `$in`,
		Restat:          true,
		SandboxDisabled: true,
	},
	"kotlincFlags", "composePluginFlag", "kotlincPluginFlags", "classpath", "srcJars", "commonSrcFilesArg", "srcJarDir",
	"classesDir", "headerClassesDir", "headerJar", "kotlinJvmTarget", "kotlinBuildFile", "emptyDir",
	"newStateFile", "priorStateFile", "sourceDeltaFile", "name", "jarArgs", "friendPathsArg")

var kotlinJarSnapshot = pctx.AndroidRemoteStaticRule("kotlin-jar-snapshot", android.RemoteRuleSupports{},
	blueprint.RuleParams{
		Command: `${config.KotlinJarSnapshotterBinary} -jar="$in"`,
		CommandDeps: []string{
			"${config.KotlinJarSnapshotterBinary}",
		},
		Restat:          true,
		SandboxDisabled: true,
	},
)

var kotlinIncremental = pctx.AndroidRemoteStaticRule("kotlin-incremental", android.RemoteRuleSupports{},
	blueprint.RuleParams{
		Pool: android.UniKotlinPool,
		Command: // Incremental

		inputDeltaCmd + ` && ` +
			`. ${config.UsePartialCompileFile} && ` +
			`if [ "$$SOONG_USE_PARTIAL_COMPILE" = "true" ]; then ` +
			`rm -rf "$srcJarDir" "$kotlinBuildFile" "$emptyDir" && ` +
			`mkdir -p "$headerClassesDir" "$emptyDir" && ` +
			kotlinZipSyncCmd + ` && ` +
			`${config.GenKotlinBuildFileCmd} --classpath "$classpath" $friendPathsArg --name "$name"` +
			` --out_dir "$classesDir" --srcs "$out.rsp" --srcs "$srcJarDir/list"` +
			` $commonSrcFilesArg --out "$kotlinBuildFile" && ` +
			`${config.KotlinIncrementalClientBinary} ${config.KotlincGlobalFlags} ` +
			` ${config.KotlincSuppressJDK9Warnings} ${config.KotlincHeapFlags} ` +
			` $kotlincFlags $composeEmbeddablePluginFlag $kotlincPluginFlags ` +
			` -jvm-target $kotlinJvmTarget -build-file=$kotlinBuildFile ` +
			` -source-delta-file=$sourceDeltaFile` +
			` -kotlin-home=$emptyDir ` +
			` -root-dir=$incrementalRootDir` +
			` -src-jars-dir=$srcJarDir` +
			` -output-dir=$outputDir` +
			` -build-dir=$buildDir ` +
			` -working-dir=$workDir ` +
			` -Xplugin=${config.KotlinAbiGenPluginJar} ` +
			` -P plugin:org.jetbrains.kotlin.jvm.abi:outputDir=$headerClassesDir && ` +
			// abi-gen doesn't delete headers for deleted source files.
			// To compensate, we diff the classesDir with the headerClassesDir and delete any
			// header files that should no longer be there.
			` EXISTING_CLASSES=$$(mktemp -p $classesDir) && ` +
			` EXISTING_HEADER_CLASSES=$$(mktemp -p $headerClassesDir) && ` +
			` (cd "$classesDir" && find . -type f | sort) > $$EXISTING_CLASSES && ` +
			` (cd "$headerClassesDir" && find . -type f | sort) > $$EXISTING_HEADER_CLASSES && ` +
			` comm -13 "$$EXISTING_CLASSES" "$$EXISTING_HEADER_CLASSES" ` +
			`   | while read -r filename; do rm "$headerClassesDir/$$filename"; done && ` +
			` rm $$EXISTING_CLASSES && rm -f $$EXISTING_HEADER_CLASSES && ` +
			`${config.SoongZipCmd} -jar $jarArgs -o $out -C $classesDir -D $classesDir -write_if_changed && ` +
			`${config.SoongZipCmd} -jar $jarArgs -o $headerJar -C $headerClassesDir -D $headerClassesDir -write_if_changed && ` +
			`rm -rf "$srcJarDir" ; ` +

			// Else non incremental
			`else ` +
			nonIncKotlinCmd + ` ; ` +
			`fi && ` +
			moveDeltaStateFile,
		CommandDeps: []string{
			"${config.FindInputDeltaCmd}",
			"${config.KotlincCmd}",
			"${config.KotlinIncrementalClientBinary}",
			"${config.KotlinCompilerJar}",
			"${config.KotlinPreloaderJar}",
			"${config.KotlinReflectJar}",
			"${config.KotlinScriptRuntimeJar}",
			"${config.KotlinStdlibJar}",
			"${config.KotlinTrove4jJar}",
			"${config.KotlinAnnotationJar}",
			"${config.KotlinAbiGenPluginJar}",
			"${config.GenKotlinBuildFileCmd}",
			"${config.SoongZipCmd}",
			"${config.UsePartialCompileFile}",
			"${config.ZipSyncCmd}",
		},
		Rspfile:         "$out.rsp",
		RspfileContent:  `$in`,
		Restat:          true,
		SandboxDisabled: true,
	},
	"kotlincFlags", "composeEmbeddablePluginFlag", "composePluginFlag", "kotlincPluginFlags", "classpath", "srcJars", "commonSrcFilesArg", "srcJarDir", "incrementalRootDir",
	"classesDir", "headerClassesDir", "headerJar", "kotlinJvmTarget", "kotlinBuildFile", "emptyDir",
	"name", "outputDir", "buildDir", "workDir", "newStateFile", "priorStateFile", "sourceDeltaFile", "jarArgs", "friendPathsArg")

var kotlinKytheExtract = pctx.AndroidStaticRule("kotlinKythe",
	blueprint.RuleParams{
		Command: `rm -rf "$srcJarDir" && ` +
			kotlinZipSyncCmd + ` && ` +
			`${config.KotlinKytheExtractor} -corpus ${kytheCorpus} --srcs @$out.rsp --srcs @"$srcJarDir/list" $commonSrcFilesList --cp @$classpath -o $out --kotlin_out $outJar ` +
			// wrap the additional kotlin args.
			// Skip Xbuild file, pass the cp explicitly.
			// Skip header jars, those should not have an effect on kythe results.
			` --args '${config.KotlincGlobalFlags} ` +
			` ${config.KotlincSuppressJDK9Warnings} ${config.JavacHeapFlags} ` +
			` $kotlincFlags $friendPathsArg $kotlincPluginFlags -jvm-target $kotlinJvmTarget ` +
			`${config.KotlincKytheGlobalFlags}'`,
		CommandDeps: []string{
			"${config.KotlinKytheExtractor}",
			"${config.ZipSyncCmd}",
		},
		Rspfile:         "$out.rsp",
		RspfileContent:  "$in",
		SandboxDisabled: true,
	},
	"classpath", "kotlincFlags", "kotlincPluginFlags", "commonSrcFilesList", "kotlinJvmTarget", "outJar",
	"srcJars", "srcJarDir", "friendPathsArg",
)

var kotlinIncrementalClean = pctx.AndroidStaticRule("kotlin-partialcompileclean",
	blueprint.RuleParams{
		Command2: blueprint.NewCommand(
			android.Rm, ` -rf "$cpSnapshot" "$outDir" "$buildDir" "$workDir"`),
	},
	"cpSnapshot", "outDir", "buildDir", "workDir",
)

func kotlinCommonSrcsList(ctx android.ModuleContext, commonSrcFiles android.Paths) android.OptionalPath {
	if len(commonSrcFiles) > 0 {
		// The list of common_srcs may be too long to put on the command line, but
		// we can't use the rsp file because it is already being used for srcs.
		// Insert a second rule to write out the list of resources to a file.
		commonSrcsList := android.PathForModuleOut(ctx, "kotlinc_common_srcs.list")
		rule := android.NewRuleBuilder(pctx, ctx).SandboxDisabled()
		rule.Command().Text("cp").
			FlagWithRspFileInputList("", commonSrcsList.ReplaceExtension(ctx, "rsp"), commonSrcFiles).
			Output(commonSrcsList)
		rule.Build("kotlin_common_srcs_list", "kotlin common_srcs list")
		return android.OptionalPathForPath(commonSrcsList)
	}
	return android.OptionalPath{}
}

func jarNameToKotlinSnapshotName(ctx android.ModuleContext, jarFile android.WritablePath) android.WritablePath {
	return jarFile.ReplaceExtension(ctx, "snapshot.bin")
}

func SnapshotJarForKotlin(ctx android.ModuleContext, jarFile android.WritablePath) android.Path {
	outPath := jarNameToKotlinSnapshotName(ctx, jarFile)
	ctx.Build(pctx, android.BuildParams{
		Rule:        kotlinJarSnapshot,
		Description: "Creates a snapshot.bin for the given jar",
		Input:       jarFile,
		Output:      outPath,
	})

	return outPath
}

// kotlinCompile takes .java and .kt sources and srcJars, and compiles the .kt sources into a classes jar in outputFile.
func (j *Module) kotlinCompile(ctx android.ModuleContext, outputFile, headerOutputFile android.WritablePath,
	srcFiles, commonSrcFiles, srcJars android.Paths, flags javaBuilderFlags, manifest android.OptionalPath,
	compileData KotlinCompileData, incremental bool) {

	var deps android.Paths
	var orderOnlyDeps android.Paths
	deps = append(deps, flags.kotlincClasspath...)
	deps = append(deps, flags.kotlincDeps...)
	deps = append(deps, commonSrcFiles...)

	kotlinName := filepath.Join(ctx.ModuleDir(), ctx.ModuleSubDir(), ctx.ModuleName())
	kotlinName = strings.ReplaceAll(kotlinName, "/", "__")

	commonSrcsList := kotlinCommonSrcsList(ctx, commonSrcFiles)
	commonSrcFilesArg := ""
	if commonSrcsList.Valid() {
		deps = append(deps, commonSrcsList.Path())
		commonSrcFilesArg = "--common_srcs " + commonSrcsList.String()
	}

	associateJars := getAssociateJars(ctx, j.properties.Associates)
	if len(associateJars) > 0 {
		flags.kotlincFriendPathsArg = " -Xfriend-paths=" + strings.Join(associateJars.Strings(), ",")
		deps = append(deps, associateJars...)

		// Prepend the associates classes jar in the classpath, so that they take priority over the other jars.
		var newClasspath classpath
		newClasspath = append(newClasspath, associateJars...)
		newClasspath = append(newClasspath, flags.kotlincClasspath...)
		flags.kotlincClasspath = newClasspath
	}

	classpathRspFile := android.PathForModuleOut(ctx, "kotlinc", "classpath.rsp")
	android.WriteFileRule(ctx, classpathRspFile, strings.Join(flags.kotlincClasspath.Strings(), " "))
	deps = append(deps, classpathRspFile)

	if flags.strictDepsLevel != "" && len(flags.directClasspath) > 0 {
		rspFile := android.PathForModuleOut(ctx, "strict_deps.rsp")
		android.WriteFileRule(ctx, rspFile, strings.Join(flags.directClasspath.Strings(), "\n"))
		deps = append(deps, rspFile)

		for _, pluginJar := range flags.kotlinStrictDepsPluginJars {
			flags.kotlincFlags += " -Xplugin=" + pluginJar.String()
			deps = append(deps, pluginJar)
		}
		flags.kotlincFlags += " -P plugin:com.android.strictdeps:rsp=" + rspFile.String()
		flags.kotlincFlags += " -P plugin:com.android.strictdeps:level=" + flags.strictDepsLevel
	}

	if incremental {
		var snapshotDeps android.Paths
		// Check that we have a snapshot.bin for each jar, and include them as needed.
		for _, dep := range deps.FilterByExt(".jar") {
			if snf, exists := flags.kSnapshotFiles[dep.String()]; exists {
				snapshotDeps = append(snapshotDeps, snf)
			} else {
				if _, ok := dep.(android.WritablePath); !ok {
					// Some prebuilts fall through into here. This means updates to those
					// won't cause proper kotlin recompilation. This is fixable by `m clean`
					// The prebuilts that cause this are few in number, and updating them
					// generally means _everything_ needs to be rebuilt anyways.
					// TODO: figure out where to snapshot prebuilts.
					//fmt.Printf("Not writable: %s\n", dep.String())
				} else if strings.Contains(dep.String(), "/gen/") {
					//fmt.Printf("No snapshots for java_genrule yet: %s\n", dep.String())
				} else {
					// This _should_ cause an error, since there is no rule to generate the snapshot
					// file needed. If there was, we wouldn't be in this else block.
					snapshotName := jarNameToKotlinSnapshotName(ctx, dep.(android.WritablePath))
					//fmt.Printf("Wanted but not found: %s\n", dep.String())
					ctx.ModuleErrorf("\nKotlin Snapshot for jar missing: %s\nWanted: %s", dep.String(), snapshotName)
					// Track these deps to ensure that ninja will also fail.
					snapshotDeps = append(snapshotDeps, snapshotName)
				}
			}
		}

		deps = append(deps, snapshotDeps...)
	}
	// Don't add srcJars as deps until after checking for snapshots.
	// Taking kotlin snapshots of sources doesn't make sense, as they aren't depended upon as
	// libraries. Their contents are inspected by find_input_delta directly.
	deps = append(deps, srcJars...)

	var jarArgs string
	if manifest.Valid() {
		jarArgs = "-m " + manifest.Path().String()
		deps = append(deps, manifest.Path())
	}

	rule := kotlinc
	description := "kotlinc"
	incrementalRootDir := android.PathForModuleOut(ctx, "kotlinc")
	outputDir := "classes"
	buildDir := "build"
	workDir := "work"
	srcJarDir := "srcJars"
	args := map[string]string{
		"classpath":          classpathRspFile.String(),
		"friendPathsArg":     flags.kotlincFriendPathsArg,
		"kotlincFlags":       flags.kotlincFlags,
		"kotlincPluginFlags": flags.kotlincPluginFlags,
		"commonSrcFilesArg":  commonSrcFilesArg,
		"srcJars":            strings.Join(srcJars.Strings(), " "),
		"classesDir":         android.PathForModuleOut(ctx, "kotlinc", outputDir).String(),
		"headerClassesDir":   android.PathForModuleOut(ctx, "kotlinc", "header_classes").String(),
		"headerJar":          headerOutputFile.String(),
		"srcJarDir":          android.PathForModuleOut(ctx, "kotlinc", srcJarDir).String(),
		"kotlinBuildFile":    android.PathForModuleOut(ctx, "kotlinc-build.xml").String(),
		"emptyDir":           android.PathForModuleOut(ctx, "kotlinc", "empty").String(),
		"kotlinJvmTarget":    flags.javaVersion.StringForKotlinc(),
		"name":               kotlinName,
		"newStateFile":       compileData.pcStateFileNew.String(),
		"priorStateFile":     compileData.pcStateFilePrior.String(),
		"sourceDeltaFile":    compileData.diffFile.String(),
		"composePluginFlag":  flags.composePluginFlag,
		"jarArgs":            jarArgs,
	}
	if incremental {
		rule = kotlinIncremental
		description = "kotlin-incremental"
		args["outputDir"] = outputDir
		args["buildDir"] = buildDir
		args["workDir"] = workDir
		args["incrementalRootDir"] = incrementalRootDir.String()
		args["sourceDeltaFile"] = compileData.diffFile.String()
		args["composeEmbeddablePluginFlag"] = flags.composeEmbeddablePluginFlag
	}

	ctx.Build(pctx, android.BuildParams{
		Rule:            rule,
		Description:     description,
		Inputs:          srcFiles,
		Implicits:       deps,
		OrderOnly:       orderOnlyDeps,
		Output:          outputFile,
		ImplicitOutputs: []android.WritablePath{headerOutputFile, compileData.pcStateFilePrior},
		Args:            args,
	})

	cleanPhonyPath := android.PathForModuleOut(ctx, "partialcompileclean", kotlinName)
	ctx.Build(pctx, android.BuildParams{
		Rule:        kotlinIncrementalClean,
		Description: "kotlin-partialcompileclean",
		Output:      cleanPhonyPath,
		Inputs:      android.Paths{},
		Args: map[string]string{
			// TODO: add this cp snapshot as a param to the incremental compiler.
			"cpSnapshot": incrementalRootDir.Join(ctx, "shrunk-classpath-snapshot.bin").String(),
			"outDir":     incrementalRootDir.Join(ctx, outputDir).String(),
			"buildDir":   incrementalRootDir.Join(ctx, buildDir).String(),
			"workDir":    incrementalRootDir.Join(ctx, workDir).String(),
		},
		PhonyOutput: true,
	})
	ctx.Phony("partialcompileclean", cleanPhonyPath)

	// Emit kythe xref rule
	if (ctx.Config().EmitXrefRules()) && ctx.IsPrimaryModule() {
		extractionFile := outputFile.ReplaceExtension(ctx, "kzip")
		args := map[string]string{
			"classpath":          classpathRspFile.String(),
			"friendPathsArg":     flags.kotlincFriendPathsArg,
			"kotlincFlags":       flags.kotlincFlags,
			"kotlincPluginFlags": flags.kotlincPluginFlags,
			"kotlinJvmTarget":    flags.javaVersion.StringForKotlinc(),
			"outJar":             outputFile.String(),
			"srcJars":            strings.Join(srcJars.Strings(), " "),
			"srcJarDir":          android.PathForModuleOut(ctx, "kotlinc", "srcJars.xref").String(),
		}
		if commonSrcsList.Valid() {
			args["commonSrcFilesList"] = "--common_srcs @" + commonSrcsList.String()
		}
		ctx.Build(pctx, android.BuildParams{
			Rule:        kotlinKytheExtract,
			Description: "kotlinKythe",
			Output:      extractionFile,
			Inputs:      srcFiles,
			Implicits:   deps,
			Args:        args,
		})
		j.kytheKotlinFiles = append(j.kytheKotlinFiles, extractionFile)
	}
}

func getAssociateJars(ctx android.ModuleContext, associates []string) android.Paths {
	if len(associates) == 0 {
		return nil
	}

	associatesFound := make(map[string]bool)
	for _, name := range associates {
		associatesFound[name] = false
	}

	var associateJars android.Paths
	ctx.VisitDirectDepsProxy(func(depModule android.ModuleProxy) {
		depName := ctx.OtherModuleName(depModule)
		_, isAssociate := associatesFound[depName]
		if !isAssociate {
			return
		}

		associatesFound[depName] = true
		depInfo, ok := android.OtherModuleProvider(ctx, depModule, JavaInfoProvider)
		if !ok {
			ctx.PropertyErrorf("Associates", "associate module '%s' is not a Java module", depName)
			return
		}

		if len(depInfo.KotlinHeaderJars) == 0 {
			ctx.PropertyErrorf("Associates", "associate module '%s' is not a Kotlin module", depName)
			return
		}

		associateJars = append(associateJars, depInfo.KotlinHeaderJars...)
	})

	for name, found := range associatesFound {
		if !found {
			ctx.PropertyErrorf("Associates", "associate module '%s' must also be listed as a direct dependency (e.g. in static_libs or libs)", name)
		}
	}

	return associateJars
}

var kspIncrementalClean = pctx.AndroidStaticRule("ksp-partialcompileclean",
	blueprint.RuleParams{
		Command2: blueprint.NewCommand(android.Rm, ` -rf "$kspDir/out/caches"`),
	},
	"kspDir")

var kspProcessingRule = pctx.AndroidRemoteStaticRule("ksp", android.RemoteRuleSupports{},
	blueprint.RuleParams{
		Pool: android.UniKotlinPool,
		Command: `mkdir -p "$kspDir/out/java" "$kspDir/out/caches" "$kspDir/out/classes" "$kspDir/out/kotlin" "$kspDir/out/resources" && ` +
			` . ${config.UsePartialCompileFile} && ` +
			inputDeltaCmd + ` && ` +
			kotlinZipSyncCmd + ` && ` +
			`${config.KotlinKspClientBinary} ` +
			` -jvm-target=$kotlinJvmTarget ` +
			` -project-base-dir=. ` +
			` -module-name=$name ` +
			` -source-roots=@$out.rsp:@$srcJarDir/list ` +
			` -src-jars-dir=$srcJarDir` +
			` -libraries=@$classpath ` +
			` -friends=$friendArg ` +
			` -common-src-roots=$commonSrcRootArg ` +
			` -output-base-dir=$kspDir/out ` +
			` -java-output-dir=$kspDir/out/java ` +
			` -caches-dir=$kspDir/out/caches ` +
			` -class-output-dir=$kspDir/out/classes ` +
			` -kotlin-output-dir=$kspDir/out/kotlin ` +
			` -resource-output-dir=$kspDir/out/resources ` +
			` -source-delta-file=$sourceDeltaFile ` +
			` -incremental=$$([ "$$SOONG_USE_PARTIAL_COMPILE" = "true" ] && echo "true" || echo "false") ` +
			` -language-version=2.2 ` +
			` -api-version=2.2 ` +
			` -processor-options=$processorOptions ` +
			` $kspProcessorPath ` +
			`&& ` +
			`${config.SoongZipCmd} -jar -write_if_changed -o $out -C $kspDir/out/java -D $kspDir/out/java && ` +
			`${config.SoongZipCmd} -jar -write_if_changed -o $kotlinSrcJarOutputFile -C $kspDir/out/kotlin -D $kspDir/out/kotlin && ` +
			`${config.SoongZipCmd} -jar -write_if_changed -o $resJarOutputFile -C $kspDir/out/resources -D $kspDir/out/resources && ` +
			`${config.SoongZipCmd} -jar -write_if_changed -o $classJarOutputFile -C $kspDir/out/classes -D $kspDir/out/classes && ` +
			`if [ "$$SOONG_USE_PARTIAL_COMPILE" != "true" ]; then ` +
			`  rm -rf "$kspDir/out/*"; ` +
			`fi  && ` +
			moveDeltaStateFile,
		CommandDeps: []string{
			"${config.FindInputDeltaCmd}",
			"${config.GenKotlinBuildFileCmd}",
			"${config.JavaCmd}",
			"${config.KotlincCmd}",
			"${config.KotlinCompilerJar}",
			"${config.KotlinKspClientBinary}",
			"${config.SoongZipCmd}",
			"${config.UsePartialCompileFile}",
			"${config.ZipSyncCmd}",
		},
		Rspfile:         "$out.rsp",
		RspfileContent:  `$in`,
		Restat:          true,
		SandboxDisabled: true,
	},
	"kotlincFlags", "kspProcessorPath", "kotlinJvmTarget",
	"classpath", "fcp", "srcJars", "commonSrcFilesArg", "srcJarDir", "kspDir",
	"kotlinBuildFile", "name", "friendPathsArg", "friendArg", "processorOptions",
	"commonSrcRootArg", "kotlinSrcJarOutputFile", "resJarOutputFile", "classJarOutputFile",
	"newStateFile", "priorStateFile", "sourceDeltaFile")

var kaptStubs = pctx.AndroidRemoteStaticRule("kaptStubs", android.RemoteRuleSupports{},
	blueprint.RuleParams{
		Pool: android.UniKotlinPool,
		Command: `rm -rf "$srcJarDir" "$kotlinBuildFile" "$kaptDir" && ` +
			`mkdir -p "$srcJarDir" "$kaptDir/sources" "$kaptDir/classes" && ` +
			// Only include java files in our src jar, so don't use kotlinZipSyncCmd
			`${config.ZipSyncCmd} -d $srcJarDir -l $srcJarDir/list -f "*.java" $srcJars && ` +
			`${config.FindInputDeltaCmd} --template '' --target "$out" --inputs_file "$out.rsp" && ` +
			`${config.GenKotlinBuildFileCmd} --classpath "$classpath" $friendPathsArg --name "$name"` +
			` --srcs "$out.rsp" --srcs "$srcJarDir/list"` +
			` $commonSrcFilesArg --out "$kotlinBuildFile" && ` +
			`${config.KotlincCmd} ${config.KotlincGlobalFlags} ` +
			`${config.KaptSuppressJDK9Warnings} ${config.KotlincSuppressJDK9Warnings} ` +
			`${config.JavacHeapFlags} $kotlincFlags ` +
			`-Xplugin=${config.KotlinKaptJar} ` +
			`-P plugin:org.jetbrains.kotlin.kapt3:sources=$kaptDir/sources ` +
			`-P plugin:org.jetbrains.kotlin.kapt3:classes=$kaptDir/classes ` +
			`-P plugin:org.jetbrains.kotlin.kapt3:stubs=$kaptDir/stubs ` +
			`-P plugin:org.jetbrains.kotlin.kapt3:correctErrorTypes=true ` +
			`-P plugin:org.jetbrains.kotlin.kapt3:aptMode=stubs ` +
			`-P plugin:org.jetbrains.kotlin.kapt3:javacArguments=$encodedJavacFlags ` +
			`$kaptProcessorPath ` +
			`$kaptProcessor ` +
			`-Xbuild-file=$kotlinBuildFile && ` +
			`${config.SoongZipCmd} -jar -write_if_changed -o $out -C $kaptDir/stubs -D $kaptDir/stubs && ` +
			`rm -rf "$srcJarDir"`,
		CommandDeps: []string{
			"${config.FindInputDeltaCmd}",
			"${config.KotlincCmd}",
			"${config.KotlinCompilerJar}",
			"${config.KotlinKaptJar}",
			"${config.GenKotlinBuildFileCmd}",
			"${config.SoongZipCmd}",
			"${config.ZipSyncCmd}",
		},
		Rspfile:         "$out.rsp",
		RspfileContent:  `$in`,
		Restat:          true,
		SandboxDisabled: true,
	},
	"kotlincFlags", "encodedJavacFlags", "kaptProcessorPath", "kaptProcessor",
	"classpath", "srcJars", "commonSrcFilesArg", "srcJarDir", "kaptDir",
	"kotlinBuildFile", "name", "classesJarOut", "friendPathsArg")

func (j *Module) kotlinKsp(ctx android.ModuleContext, javaSrcJarOutputFile, kotlinSrcJarOutputFile,
	resJarOutputFile, classJarOutputFile android.WritablePath, processorFlags []string,
	srcFiles, commonSrcFiles, srcJars android.Paths, compileData KotlinCompileData,
	flags javaBuilderFlags) {

	srcFiles = append(android.Paths(nil), srcFiles...)

	var deps android.Paths
	deps = append(deps, flags.kotlincClasspath...)
	deps = append(deps, flags.kotlincDeps...)
	deps = append(deps, srcJars...)
	deps = append(deps, flags.processorPath...)
	deps = append(deps, commonSrcFiles...)

	commonSrcsList := kotlinCommonSrcsList(ctx, commonSrcFiles)
	commonSrcFilesArg := ""
	commonSrcRootArg := ""
	if commonSrcsList.Valid() {
		deps = append(deps, commonSrcsList.Path())
		commonSrcFilesArg = "--common_srcs " + commonSrcsList.String()
		commonSrcRootArg = strings.Join(commonSrcFiles.Strings(), ":")
	}

	kspName := filepath.Join(ctx.ModuleDir(), ctx.ModuleSubDir(), ctx.ModuleName(), "ksp")
	kspName = strings.ReplaceAll(kspName, "/", "__")

	classpathRspFile := android.PathForModuleOut(ctx, "ksp", "classpath.rsp")
	android.WriteFileRule(ctx, classpathRspFile, strings.Join(flags.kotlincClasspath.Strings(), "\n"))
	deps = append(deps, classpathRspFile)
	associateJars := getAssociateJars(ctx, j.properties.Associates)
	friendArg := ""
	if len(associateJars) > 0 {
		friendArg = strings.Join(associateJars.Strings(), ":")
	}

	kspDir := android.PathForModuleOut(ctx, "ksp")
	ctx.Build(pctx, android.BuildParams{
		Rule:            kspProcessingRule,
		Description:     "ksp annotation processing",
		Output:          javaSrcJarOutputFile,
		ImplicitOutputs: android.WritablePaths{kotlinSrcJarOutputFile, resJarOutputFile, classJarOutputFile},
		Inputs:          append(srcFiles, srcJars...),
		Implicits:       deps,
		Args: map[string]string{
			"classpath":              classpathRspFile.String(),
			"fcp":                    flags.kotlincClasspath.FormJavaClassPath(""),
			"friendPathsArg":         flags.kotlincFriendPathsArg,
			"friendArg":              friendArg,
			"kotlincFlags":           flags.kotlincFlags,
			"kotlinJvmTarget":        flags.javaVersion.StringForKotlinc(),
			"commonSrcFilesArg":      commonSrcFilesArg,
			"commonSrcRootArg":       commonSrcRootArg,
			"srcJars":                strings.Join(srcJars.Strings(), " "),
			"srcJarDir":              kspDir.Join(ctx, "srcJars").String(),
			"processorOptions":       strings.Join(processorFlags, ":"),
			"kspProcessorPath":       strings.Join(flags.processorPath.Strings(), " "),
			"kspDir":                 kspDir.String(),
			"name":                   kspName,
			"kotlinSrcJarOutputFile": kotlinSrcJarOutputFile.String(),
			"resJarOutputFile":       resJarOutputFile.String(),
			"classJarOutputFile":     classJarOutputFile.String(),
			"newStateFile":           compileData.pcStateFileNew.String(),
			"priorStateFile":         compileData.pcStateFilePrior.String(),
			"sourceDeltaFile":        compileData.diffFile.String(),
		},
	})

	cleanPhonyPath := android.PathForModuleOut(ctx, "partialcompileclean", kspName)
	ctx.Build(pctx, android.BuildParams{
		Rule:        kspIncrementalClean,
		Description: "ksp-partialcompileclean",
		Output:      cleanPhonyPath,
		Inputs:      android.Paths{},
		Args: map[string]string{
			"kspDir": kspDir.String(),
		},
		PhonyOutput: true,
	})
	ctx.Phony("partialcompileclean", cleanPhonyPath)
}

// kotlinKapt performs Kotlin-compatible annotation processing.  It takes .kt and .java sources and srcjars, and runs
// annotation processors over all of them, producing a srcjar of generated code in outputFile.  The srcjar should be
// added as an additional input to kotlinc and javac rules, and the javac rule should have annotation processing
// disabled.
func kotlinKapt(ctx android.ModuleContext, srcJarOutputFile, resJarOutputFile android.WritablePath,
	srcFiles, commonSrcFiles, srcJars android.Paths,
	flags javaBuilderFlags) {

	srcFiles = append(android.Paths(nil), srcFiles...)

	var deps android.Paths
	deps = append(deps, flags.kotlincClasspath...)
	deps = append(deps, flags.kotlincDeps...)
	deps = append(deps, srcJars...)
	deps = append(deps, flags.processorPath...)
	deps = append(deps, commonSrcFiles...)

	commonSrcsList := kotlinCommonSrcsList(ctx, commonSrcFiles)
	commonSrcFilesArg := ""
	if commonSrcsList.Valid() {
		deps = append(deps, commonSrcsList.Path())
		commonSrcFilesArg = "--common_srcs " + commonSrcsList.String()
	}

	kaptProcessorPath := flags.processorPath.FormRepeatedClassPath("-P plugin:org.jetbrains.kotlin.kapt3:apclasspath=")

	kaptProcessor := ""
	for i, p := range flags.processors {
		if i > 0 {
			kaptProcessor += " "
		}
		kaptProcessor += "-P plugin:org.jetbrains.kotlin.kapt3:processors=" + p
	}

	encodedJavacFlags := kaptEncodeFlags([][2]string{
		{"-source", flags.javaVersion.String()},
		{"-target", flags.javaVersion.String()},
	})

	kotlinName := filepath.Join(ctx.ModuleDir(), ctx.ModuleSubDir(), ctx.ModuleName())
	kotlinName = strings.ReplaceAll(kotlinName, "/", "__")

	classpathRspFile := android.PathForModuleOut(ctx, "kapt", "classpath.rsp")
	android.WriteFileRule(ctx, classpathRspFile, strings.Join(flags.kotlincClasspath.Strings(), "\n"))
	deps = append(deps, classpathRspFile)

	// First run kapt to generate .java stubs from .kt files
	kaptStubsJar := android.PathForModuleOut(ctx, "kapt", "stubs.jar")
	ctx.Build(pctx, android.BuildParams{
		Rule:        kaptStubs,
		Description: "kapt stubs",
		Output:      kaptStubsJar,
		Inputs:      srcFiles,
		Implicits:   deps,
		Args: map[string]string{
			"classpath":         classpathRspFile.String(),
			"friendPathsArg":    flags.kotlincFriendPathsArg,
			"kotlincFlags":      flags.kotlincFlags,
			"commonSrcFilesArg": commonSrcFilesArg,
			"srcJars":           strings.Join(srcJars.Strings(), " "),
			"srcJarDir":         android.PathForModuleOut(ctx, "kapt", "srcJars").String(),
			"kotlinBuildFile":   android.PathForModuleOut(ctx, "kapt", "build.xml").String(),
			"kaptProcessorPath": strings.Join(kaptProcessorPath, " "),
			"kaptProcessor":     kaptProcessor,
			"kaptDir":           android.PathForModuleOut(ctx, "kapt/gen").String(),
			"encodedJavacFlags": encodedJavacFlags,
			"name":              kotlinName,
			"classesJarOut":     resJarOutputFile.String(),
		},
	})

	// Then run turbine to perform annotation processing on the stubs and any .java srcFiles.
	javaSrcFiles := srcFiles.FilterByExt(".java")
	turbineSrcJars := append(android.Paths{kaptStubsJar}, srcJars...)
	TurbineApt(ctx, srcJarOutputFile, resJarOutputFile, javaSrcFiles, turbineSrcJars, flags)
}

// kapt converts a list of key, value pairs into a base64 encoded Java serialization, which is what kapt expects.
func kaptEncodeFlags(options [][2]string) string {
	buf := &bytes.Buffer{}

	binary.Write(buf, binary.BigEndian, uint32(len(options)))
	for _, option := range options {
		binary.Write(buf, binary.BigEndian, uint16(len(option[0])))
		buf.WriteString(option[0])
		binary.Write(buf, binary.BigEndian, uint16(len(option[1])))
		buf.WriteString(option[1])
	}

	header := &bytes.Buffer{}
	header.Write([]byte{0xac, 0xed, 0x00, 0x05}) // java serialization header

	if buf.Len() < 256 {
		header.WriteByte(0x77) // blockdata
		header.WriteByte(byte(buf.Len()))
	} else {
		header.WriteByte(0x7a) // blockdatalong
		binary.Write(header, binary.BigEndian, uint32(buf.Len()))
	}

	return base64.StdEncoding.EncodeToString(append(header.Bytes(), buf.Bytes()...))
}

type KSnapshotContainer interface {
	JarToSnapshotMap() map[string]android.Path
}
