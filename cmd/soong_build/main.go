// Copyright 2015 Google Inc. All rights reserved.
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

package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"time"

	"android/soong/android"
	"android/soong/android/allowlists"
	"android/soong/shared"

	androidProtobuf "google.golang.org/protobuf/android"

	"github.com/google/blueprint"
	"github.com/google/blueprint/bootstrap"
	"github.com/google/blueprint/deptools"
	"github.com/google/blueprint/metrics"
	"github.com/google/blueprint/pathtools"
	"github.com/google/blueprint/proptools"
)

var (
	topDir           string
	availableEnvFile string
	usedEnvFile      string

	delveListen string
	delvePath   string
	statusFile  string

	cmdlineArgs android.CmdArgs
)

const configCacheFile = "config.cache"

type ConfigCache struct {
	EnvDepsHash                  proptools.Hash
	ProductVariableFileTimestamp int64
	SoongBuildFileTimestamp      int64
	KatiEnabled                  bool
	KatiSuffix                   string
}

func init() {
	// Flags that make sense in every mode
	flag.StringVar(&topDir, "top", "", "Top directory of the Android source tree")
	flag.StringVar(&cmdlineArgs.SoongOutDir, "soong_out", "", "Soong output directory (usually $TOP/out/soong)")
	flag.StringVar(&availableEnvFile, "available_env", "", "File containing available environment variables")
	flag.StringVar(&usedEnvFile, "used_env", "", "File containing used environment variables")
	flag.StringVar(&cmdlineArgs.OutDir, "out", "", "the ninja builddir directory")
	flag.StringVar(&cmdlineArgs.ModuleListFile, "l", "", "file that lists filepaths to parse")
	flag.StringVar(&statusFile, "status-file", "", "file used to report analysis progress")
	flag.StringVar(&cmdlineArgs.KatiSuffix, "kati_suffix", "", "the suffix for kati and ninja files, so that different configurations don't clobber each other")
	flag.BoolVar(&cmdlineArgs.KatiEnabled, "kati_enabled", false, "If the main kati build phase is enabled. False for soong-only builds")

	// Debug flags
	flag.StringVar(&delveListen, "delve_listen", "", "Delve port to listen on for debugging")
	flag.StringVar(&delvePath, "delve_path", "", "Path to Delve. Only used if --delve_listen is set")
	flag.StringVar(&cmdlineArgs.Cpuprofile, "cpuprofile", "", "write cpu profile to file")
	flag.StringVar(&cmdlineArgs.TraceFile, "trace", "", "write trace to file")
	flag.StringVar(&cmdlineArgs.Memprofile, "memprofile", "", "write memory profile to file")
	flag.BoolVar(&cmdlineArgs.NoGC, "nogc", false, "turn off GC for debugging")

	// Flags representing various modes soong_build can run in
	flag.StringVar(&cmdlineArgs.DocFile, "soong_docs", "", "build documentation file to output")
	flag.StringVar(&cmdlineArgs.OutFile, "o", "build.ninja", "the Ninja file to output")
	flag.StringVar(&cmdlineArgs.SoongVariables, "soong_variables", "soong.variables", "the file contains all build variables")
	flag.BoolVar(&cmdlineArgs.EmptyNinjaFile, "empty-ninja-file", false, "write out a 0-byte ninja file")
	flag.BoolVar(&cmdlineArgs.BuildFromSourceStub, "build-from-source-stub", false, "build Java stubs from source files instead of API text files")
	flag.BoolVar(&cmdlineArgs.EnsureAllowlistIntegrity, "ensure-allowlist-integrity", false, "verify that allowlisted modules are mixed-built")
	flag.StringVar(&cmdlineArgs.ModuleDebugFile, "soong_module_debug", "", "soong module debug info file to write")
	// Flags that probably shouldn't be flags of soong_build, but we haven't found
	// the time to remove them yet
	flag.BoolVar(&cmdlineArgs.RunGoTests, "t", false, "build and run go tests during bootstrap")
	flag.BoolVar(&cmdlineArgs.IncrementalBuildActions, "incremental-build-actions", false, "generate build actions incrementally")
	flag.StringVar(&cmdlineArgs.PartialAnalysisTargets, "partial-analysis-targets", "", "partial analysis targets")
	flag.BoolVar(&cmdlineArgs.IncrementalProviderTest, "incremental-provider-test", false, "test incremental providers restoring")
	flag.StringVar(&cmdlineArgs.IncrementalDebugFile, "incremental-debug-file", "", "incremental debug file")

	// Disable deterministic randomization in the protobuf package, so incremental
	// builds with unrelated Soong changes don't trigger large rebuilds (since we
	// write out text protos in command lines, and command line changes trigger
	// rebuilds).
	androidProtobuf.DisableRand()
}

type analysisProgress struct {
	file         string
	blueprintCnt int
	mutex        sync.Mutex
	lastStage    string
}

type analysisEventHookContext interface {
	SetEventStartedHook(func(string))
	SetEventProgressHook(func(string, int, int))
}

func newAnalysisProgress(file, moduleListFile string) *analysisProgress {
	p := &analysisProgress{file: file}
	if data, err := os.ReadFile(shared.JoinPath(topDir, moduleListFile)); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			if line != "" {
				p.blueprintCnt++
			}
		}
	}
	return p
}

func (p *analysisProgress) report(message string) {
	if p.file == "" {
		return
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if message == p.lastStage {
		return
	}
	p.lastStage = message
	if err := os.MkdirAll(filepath.Dir(p.file), 0777); err != nil {
		return
	}
	f, err := os.OpenFile(p.file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "stage\t%s\n", message)
	_ = f.Close()
}

func (p *analysisProgress) eventStarted(event string) {
	switch event {
	case "list_modules":
		p.report("Loading Android.bp file list...")
	case "parse_bp":
		p.report(fmt.Sprintf("Parsing %d Android.bp files...", p.blueprintCnt))
	case "resolve_deps":
		p.report("Resolving module dependencies and variants...")
	case "restore_build_actions":
		p.report("Restoring incremental analysis cache...")
	case "prepare_build_actions":
		p.report("Preparing build actions...")
	case "generateModuleBuildActions":
		p.report("Generating module build rules...")
	case "generateParallelSingletonBuildActions", "generateSingletonBuildActions":
		p.report("Generating global build rules...")
	case "modules":
		p.report("Writing module Ninja buckets...")
	case "singletons":
		p.report("Writing global Ninja rules...")
	case "cache_build_actions":
		p.report("Saving incremental analysis cache...")
	case "cache_module_build_actions":
		p.report("Caching module build actions...")
	case "cache_ninja_statements":
		p.report("Flushing module Ninja statement cache...")
	}
}

func (p *analysisProgress) eventProgress(event string, completed, total int) {
	if p.file == "" {
		return
	}
	p.mutex.Lock()
	defer p.mutex.Unlock()
	if err := os.MkdirAll(filepath.Dir(p.file), 0777); err != nil {
		return
	}
	f, err := os.OpenFile(p.file, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0666)
	if err != nil {
		return
	}
	_, _ = fmt.Fprintf(f, "progress\t%s\t%d\t%d\n", event, completed, total)
	_ = f.Close()
}

func newNameResolver(config android.Config) *android.NameResolver {
	return android.NewNameResolver(config)
}

func newContext(configuration android.Config) *android.Context {
	ctx := android.NewContext(configuration)
	ctx.SetNameInterface(newNameResolver(configuration))
	ctx.SetAllowMissingDependencies(configuration.AllowMissingDependencies())
	ctx.AddSourceRootDirs(configuration.SourceRootDirs()...)
	return ctx
}

func needToWriteNinjaHint(ctx *android.Context) bool {
	switch ctx.Config().GetenvWithDefault("SOONG_GENERATES_NINJA_HINT", "") {
	case "always":
		return true
	case "depend":
		if _, err := os.Stat(filepath.Join(topDir, ctx.Config().OutDir(), ".ninja_log")); errors.Is(err, os.ErrNotExist) {
			return true
		}
	}
	return false
}

func ninjaHintWeight(info *blueprint.WeightedOutputsModuleInfo, prioritizeR8 bool) (prioritized bool, weight int) {
	if prioritizeR8 {
		for _, rule := range info.Rules {
			switch rule {
			case "r8", "r8RE", "d8r8", "d8r8RE", "d8Incr8", "d8Incr8RE":
				return true, allowlists.HIGH_PRIORITIZED_WEIGHT
			}
		}
	}
	for prefix, candidateWeight := range allowlists.HugeModuleTypePrefixMap {
		if strings.HasPrefix(info.Type, prefix) {
			return true, candidateWeight
		}
	}
	inputSize := info.DepsCount + info.SrcsCount
	if inputSize <= allowlists.INPUT_SIZE_THRESHOLD {
		return false, 0
	}
	weight = (inputSize / allowlists.INPUT_SIZE_THRESHOLD) * allowlists.DEFAULT_PRIORITIZED_WEIGHT
	return true, min(weight, allowlists.HIGH_PRIORITIZED_WEIGHT)
}

func writeNinjaHint(ctx *android.Context) error {
	ctx.BeginEvent("ninja_hint")
	defer ctx.EndEvent("ninja_hint")
	// The current predictor focuses on reducing false negatives.
	// If there are too many false positives (e.g., most modules are marked as positive),
	// real long-running jobs cannot run early.
	// Therefore, the model should be adjusted in this case.
	// The model should also be adjusted if there are critical false negatives.
	prioritizeR8 := ctx.Config().Getenv("UNI_TASK_METADATA_FILE") != ""
	outputsMap := ctx.Context.GetWeightedOutputsFromPredicate(func(info *blueprint.WeightedOutputsModuleInfo) (bool, int) {
		return ninjaHintWeight(info, prioritizeR8)
	})
	var outputBuilder strings.Builder
	for output, weight := range outputsMap {
		outputBuilder.WriteString(fmt.Sprintf("%s,%d\n", output, weight))
	}
	weightListFile := filepath.Join(topDir, ctx.Config().OutDir(), ".ninja_weight_list")

	err := os.WriteFile(weightListFile, []byte(outputBuilder.String()), 0644)
	if err != nil {
		return fmt.Errorf("could not write ninja weight list file %s", err)
	}
	return nil
}

func writeUniR8Modules(ctx *android.Context, path string) error {
	if path == "" {
		return nil
	}
	rules := map[string]struct{}{
		"r8": {}, "r8RE": {}, "d8r8": {}, "d8r8RE": {},
		"d8Incr8": {}, "d8Incr8RE": {},
	}
	modules := ctx.Context.GetModuleNamesWithRules(rules)
	if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
		return err
	}
	data := []byte(strings.Join(modules, "\n"))
	if len(data) > 0 {
		data = append(data, '\n')
	}
	return pathtools.WriteFileIfChanged(path, data, 0666)
}

type uniTaskAction struct {
	Module   string   `json:"module"`
	Rule     string   `json:"rule"`
	TaskType string   `json:"task_type"`
	Outputs  []string `json:"outputs"`
}

func uniTaskType(rule string) string {
	lower := strings.ToLower(rule)
	switch {
	case strings.Contains(lower, "r8") || strings.Contains(lower, "d8"):
		return "r8"
	case strings.Contains(lower, "kotlin") || strings.Contains(lower, "kotlinc"):
		return "kotlinc"
	case strings.Contains(lower, "javac") || strings.Contains(lower, "turbine"):
		return "javac"
	case strings.Contains(lower, "rust"):
		return "rustc"
	case strings.Contains(lower, "clang"):
		return "clang"
	case strings.Contains(lower, "link") || lower == "ld" || strings.HasPrefix(lower, "ld"):
		return "linker"
	default:
		return "other"
	}
}

func writeUniTaskMetadata(ctx *android.Context, path string) error {
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0777); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".uni-task-metadata-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	writer := bufio.NewWriterSize(temporary, 256*1024)
	_, writeErr := writer.WriteString("{\"version\":1,\"actions\":[")
	first := true
	ctx.Context.VisitModuleBuildActions(func(action blueprint.ModuleBuildAction) {
		if writeErr != nil {
			return
		}
		data, err := json.Marshal(uniTaskAction{
			Module: action.Module, Rule: action.Rule,
			TaskType: uniTaskType(action.Rule), Outputs: action.Outputs,
		})
		if err != nil {
			writeErr = err
			return
		}
		if !first {
			if err := writer.WriteByte(','); err != nil {
				writeErr = err
				return
			}
		}
		first = false
		_, writeErr = writer.Write(data)
	})
	if writeErr == nil {
		_, writeErr = writer.WriteString("]}\n")
	}
	if writeErr == nil {
		writeErr = writer.Flush()
	}
	if writeErr == nil {
		writeErr = temporary.Sync()
	}
	if writeErr == nil {
		writeErr = temporary.Chmod(0666)
	}
	if closeErr := temporary.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return writeErr
	}
	return os.Rename(temporaryPath, path)
}

func writeMetrics(ctx *android.Context, configuration android.Config, eventHandler *metrics.EventHandler, metricsDir string) {
	if len(metricsDir) < 1 {
		fmt.Fprintf(os.Stderr, "\nMissing required env var for generating soong metrics: LOG_DIR\n")
		os.Exit(1)
	}
	metricsFile := filepath.Join(metricsDir, "soong_build_metrics.pb")
	err := android.WriteMetrics(ctx, configuration, eventHandler, metricsFile)
	maybeQuit(err, "error writing soong_build metrics %s", metricsFile)
}

func writeDepFile(outputFile string, eventHandler *metrics.EventHandler, ninjaDeps []string) {
	eventHandler.Begin("ninja_deps")
	defer eventHandler.End("ninja_deps")
	depFile := shared.JoinPath(topDir, outputFile+".d")
	err := deptools.WriteDepFile(depFile, outputFile, ninjaDeps)
	maybeQuit(err, "error writing depfile '%s'", depFile)
}

// Check if there are changes to the environment file, product variable file and
// soong_build binary, in which case no incremental will be performed. For env
// variables we check the used env file, which will be removed in soong ui if
// there is any changes to the env variables used last time, in which case the
// check below will fail and a full build will be attempted. If any new env
// variables are added in the new run, soong ui won't be able to detect it, the
// used env file check below will pass. But unless there is a soong build code
// change, in which case the soong build binary check will fail, otherwise the
// new env variables shouldn't have any affect.
func incrementalValid(config android.Config, configCacheFile string) (*ConfigCache, bool) {
	var newConfigCache ConfigCache
	data, err := os.ReadFile(shared.JoinPath(topDir, usedEnvFile))
	if err != nil {
		// Clean build
		if os.IsNotExist(err) {
			data = []byte{}
		} else {
			maybeQuit(err, "")
		}
	}

	newConfigCache.EnvDepsHash, err = proptools.CalculateHashReflection(data)
	newConfigCache.ProductVariableFileTimestamp = getFileTimestamp(filepath.Join(topDir, cmdlineArgs.SoongVariables))
	newConfigCache.SoongBuildFileTimestamp = getFileTimestamp(filepath.Join(topDir, config.HostToolDir(), "soong_build"))
	newConfigCache.KatiEnabled = cmdlineArgs.KatiEnabled
	newConfigCache.KatiSuffix = cmdlineArgs.KatiSuffix
	//TODO(b/344917959): out/soong/dexpreopt.config might need to be checked as well.

	file, err := os.Open(configCacheFile)
	if err != nil && os.IsNotExist(err) {
		return &newConfigCache, false
	}
	maybeQuit(err, "")
	defer file.Close()

	var configCache ConfigCache
	decoder := json.NewDecoder(file)
	err = decoder.Decode(&configCache)
	if err != nil {
		fmt.Printf("Failed to parse config cache: %s.  Continuing with non-incremental analysis.", err.Error())
		return &newConfigCache, false
	}

	return &newConfigCache, newConfigCache == configCache
}

func getFileTimestamp(file string) int64 {
	stat, err := os.Stat(file)
	if err == nil {
		return stat.ModTime().UnixMilli()
	} else if !os.IsNotExist(err) {
		maybeQuit(err, "")
	}
	return 0
}

func writeConfigCache(configCache *ConfigCache, configCacheFile string) {
	file, err := os.Create(configCacheFile)
	maybeQuit(err, "")
	defer file.Close()

	encoder := json.NewEncoder(file)
	err = encoder.Encode(*configCache)
	maybeQuit(err, "")
}

// runSoongOnlyBuild runs the standard Soong build in a number of different modes.
// It returns the path to the output file (usually the ninja file) and the deps that need
// to trigger a soong rerun.
func runSoongOnlyBuild(ctx *android.Context) (string, []string) {
	ctx.EventHandler.Begin("soong_build")
	defer ctx.EventHandler.End("soong_build")

	var stopBefore bootstrap.StopBefore
	switch ctx.Config().BuildMode {
	case android.GenerateDocFile:
		stopBefore = bootstrap.StopBeforePrepareBuildActions
	default:
		stopBefore = bootstrap.DoEverything
	}

	ninjaDeps, err := bootstrap.RunBlueprint(cmdlineArgs.Args, stopBefore, ctx.Context, ctx.Config())
	maybeQuit(err, "")

	// Convert the Soong module graph into Bazel BUILD files.
	switch ctx.Config().BuildMode {
	case android.GenerateDocFile:
		// TODO: we could make writeDocs() return the list of documentation files
		// written and add them to the .d file. Then soong_docs would be re-run
		// whenever one is deleted.
		err := writeDocs(ctx, shared.JoinPath(topDir, cmdlineArgs.DocFile))
		maybeQuit(err, "error building Soong documentation")
		return cmdlineArgs.DocFile, ninjaDeps
	default:
		// The actual output (build.ninja) was written in the RunBlueprint() call
		// above
		if needToWriteNinjaHint(ctx) {
			writeNinjaHint(ctx)
		}
		return cmdlineArgs.OutFile, ninjaDeps
	}
}

// soong_ui dumps the available environment variables to
// soong.environment.available . Then soong_build itself is run with an empty
// environment so that the only way environment variables can be accessed is
// using Config, which tracks access to them.

// At the end of the build, a file called soong.environment.used is written
// containing the current value of all used environment variables. The next
// time soong_ui is run, it checks whether any environment variables that was
// used had changed and if so, it deletes soong.environment.used to cause a
// rebuild.
//
// The dependency of build.ninja on soong.environment.used is declared in
// build.ninja.d
func parseAvailableEnv() map[string]string {
	if availableEnvFile == "" {
		fmt.Fprintf(os.Stderr, "--available_env not set\n")
		os.Exit(1)
	}
	result, err := shared.EnvFromFile(shared.JoinPath(topDir, availableEnvFile))
	maybeQuit(err, "error reading available environment file '%s'", availableEnvFile)
	return result
}

func configureAnalysisRuntime(availableEnv map[string]string) (int64, int) {
	memoryLimit, _ := strconv.ParseInt(availableEnv["SOONG_ANALYSIS_MEMORY_LIMIT_BYTES"], 10, 64)
	if memoryLimit > 0 {
		debug.SetMemoryLimit(memoryLimit)
	}
	gcPercent, _ := strconv.Atoi(availableEnv["SOONG_ANALYSIS_GC_PERCENT"])
	if gcPercent > 0 {
		debug.SetGCPercent(gcPercent)
	}
	return memoryLimit, gcPercent
}

func main() {
	flag.Parse()

	if cmdlineArgs.Memprofile == "" {
		// Go enables memory profile collection automatically if any references
		// are linked in to the binary.
		// Disable collection of memory profiles if we won't save one to disk.
		runtime.MemProfileRate = 0
	}
	soongStartTime := time.Now()

	shared.ReexecWithDelveMaybe(delveListen, delvePath)
	android.InitSandbox(topDir)

	availableEnv := parseAvailableEnv()
	analysisMemoryLimit, _ := configureAnalysisRuntime(availableEnv)

	if availableEnv["SOONG_ENFORCE_NO_REANALYSIS"] == "true" {
		fmt.Fprintln(os.Stderr, "Reanalysis will run due to build graph or product configuration change.")
		os.Exit(1)
	}

	configuration, err := android.NewConfig(cmdlineArgs, availableEnv)
	maybeQuit(err, "")
	if configuration.Getenv("ALLOW_MISSING_DEPENDENCIES") == "true" {
		configuration.SetAllowMissingDependencies()
	}

	// Bypass configuration.Getenv, as LOG_DIR does not need to be dependency tracked. By definition, it will
	// change between every CI build, so tracking it would require re-running Soong for every build.
	metricsDir := availableEnv["LOG_DIR"]

	ctx := newContext(configuration)
	progress := newAnalysisProgress(statusFile, cmdlineArgs.ModuleListFile)
	if hookContext, ok := any(ctx).(analysisEventHookContext); ok {
		hookContext.SetEventStartedHook(progress.eventStarted)
		hookContext.SetEventProgressHook(progress.eventProgress)
	}
	progress.report("Initializing Android.bp analysis...")
	android.StartBackgroundMetrics(configuration)

	var configCache *ConfigCache
	configFile := filepath.Join(topDir, ctx.Config().OutDir(), configCacheFile)
	incremental := false
	// Incremental analysis is valid for both Soong-only and Soong+Make builds.
	ctx.SetIncrementalEnabled(cmdlineArgs.IncrementalBuildActions)
	if ctx.GetIncrementalEnabled() {
		configCache, incremental = incrementalValid(ctx.Config(), configFile)
		ctx.SetIncrementalProviderTest(incremental && cmdlineArgs.IncrementalProviderTest)
	}
	ctx.SetIncrementalAnalysis(incremental)
	ctx.SetPartialAnalysisTargets(cmdlineArgs.PartialAnalysisTargets)
	ctx.SetIncrementalDebugFile(cmdlineArgs.IncrementalDebugFile)

	if configuration.Getenv("SOONG_SPLIT_ALL_VARIANTS") == "true" ||
		configuration.Getenv("RUN_BUILD_TESTS") == "true" ||
		// Soong does not have sufficient information to determine if an androidmk module
		// has a dependency on a non primary soong module variant.
		// Analyze all variants in soong+make builds.
		cmdlineArgs.KatiEnabled ||
		// TODO (b/477627661): Enable on demand variants with AllowMissingDependencies
		configuration.AllowMissingDependencies() {
		ctx.SetSplitAllVariants(true)
	}

	if analysisMemoryLimit > 0 {
		progress.report(fmt.Sprintf("Analyzing Android.bp files (memory limit %.1f GiB)...",
			float64(analysisMemoryLimit)/(1024*1024*1024)))
	} else {
		progress.report("Analyzing Android.bp files...")
	}
	ctx.Register()
	finalOutputFile, ninjaDeps := runSoongOnlyBuild(ctx)
	maybeQuit(writeUniR8Modules(ctx, availableEnv["UNI_R8_MODULES_FILE"]), "write uni R8 module list")
	maybeQuit(writeUniTaskMetadata(ctx, availableEnv["UNI_TASK_METADATA_FILE"]), "write uni task metadata")

	ninjaDeps = append(ninjaDeps, configuration.ProductVariablesFileName)
	ninjaDeps = append(ninjaDeps, usedEnvFile)
	if shared.IsDebugging() {
		// Add a non-existent file to the dependencies so that soong_build will rerun when the debugger is
		// enabled even if it completed successfully.
		ninjaDeps = append(ninjaDeps, filepath.Join(configuration.SoongOutDir(), "always_rerun_for_delve"))
	}

	writeDepFile(finalOutputFile, ctx.EventHandler, ninjaDeps)

	if ctx.GetIncrementalEnabled() {
		data, err := shared.EnvFileContents(configuration.EnvDeps())
		maybeQuit(err, "")
		configCache.EnvDepsHash, err = proptools.CalculateHashReflection(data)
		maybeQuit(err, "")
		writeConfigCache(configCache, configFile)
	}

	writeMetrics(ctx, configuration, ctx.EventHandler, metricsDir)

	writeUsedEnvironmentFile(configuration)

	ctx.EventHandler.Begin("writeGlobFile")
	err = ctx.WriteGlobFile(shared.JoinPath(topDir, finalOutputFile), soongStartTime)
	ctx.EventHandler.End("writeGlobFile")
	maybeQuit(err, "")

	// Touch the output file so that it's the newest file created by soong_build.
	// This is necessary because, if soong_build generated any files which
	// are ninja inputs to the main output file, then ninja would superfluously
	// rebuild this output file on the next build invocation.
	touch(shared.JoinPath(topDir, finalOutputFile))
	progress.report("Android.bp analysis complete")
}

func writeUsedEnvironmentFile(configuration android.Config) {
	if usedEnvFile == "" {
		return
	}

	path := shared.JoinPath(topDir, usedEnvFile)
	data, err := shared.EnvFileContents(configuration.EnvDeps())
	maybeQuit(err, "error writing used environment file '%s'\n", usedEnvFile)

	err = pathtools.WriteFileIfChanged(path, data, 0666)
	maybeQuit(err, "error writing used environment file '%s'", usedEnvFile)
}

func touch(path string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0666)
	maybeQuit(err, "Error touching '%s'", path)
	err = f.Close()
	maybeQuit(err, "Error touching '%s'", path)

	currentTime := time.Now().Local()
	err = os.Chtimes(path, currentTime, currentTime)
	maybeQuit(err, "error touching '%s'", path)
}

func maybeQuit(err error, format string, args ...interface{}) {
	if err == nil {
		return
	}
	if format != "" {
		fmt.Fprintln(os.Stderr, fmt.Sprintf(format, args...)+": "+err.Error())
	} else {
		fmt.Fprintln(os.Stderr, err)
	}
	os.Exit(1)
}
