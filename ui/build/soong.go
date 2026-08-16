// Copyright 2017 Google Inc. All rights reserved.
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

package build

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"android/soong/ui/tracer"

	"android/soong/ui/metrics"
	"android/soong/ui/metrics/metrics_proto"
	"android/soong/ui/status"

	"android/soong/shared"

	"github.com/google/blueprint"
	"github.com/google/blueprint/bootstrap"
	"github.com/google/blueprint/pathtools"

	"google.golang.org/protobuf/proto"
)

const (
	availableEnvFile = "soong.environment.available"
	usedEnvFile      = "soong.environment.used"

	soongBuildTag      = "build"
	jsonModuleGraphTag = "modulegraph"
	soongDocsTag       = "soong_docs"

	// bootstrapEpoch is used to determine if an incremental build is incompatible with the current
	// version of bootstrap and needs cleaning before continuing the build.  Increment this for
	// incompatible changes, for example when moving the location of a microfactory binary that is
	// executed during bootstrap before the primary builder has had a chance to update the path.
	bootstrapEpoch = 1
)

func soongAnalysisStatusFile(config Config) string {
	statusFile := filepath.Join(config.SoongOutDir(), ".analysis_status")
	if absStatusFile, err := filepath.Abs(statusFile); err == nil {
		return absStatusFile
	}
	return statusFile
}

var (
	// Used during parallel update of symlinks in out directory to reflect new
	// TOP dir.
	symlinkWg            sync.WaitGroup
	numFound, numUpdated uint32
)

func writeEnvironmentFile(_ Context, envFile string, envDeps map[string]string) error {
	data, err := shared.EnvFileContents(envDeps)
	if err != nil {
		return err
	}

	return os.WriteFile(envFile, data, 0644)
}

// This uses Android.bp files and various tools to generate <builddir>/build.ninja.
//
// However, the execution of <builddir>/build.ninja happens later in
// build/soong/ui/build/build.go#Build()
//
// We want to rely on as few prebuilts as possible, so we need to bootstrap
// Soong. The process is as follows:
//
// 1. We use "Microfactory", a simple tool to compile Go code, to build
//    first itself, then soong_ui from soong_ui.bash. This binary contains
//    parts of soong_build that are needed to build itself.
// 2. This simplified version of soong_build then reads the Blueprint files
//    that describe itself and emits .bootstrap/build.ninja that describes
//    how to build its full version and use that to produce the final Ninja
//    file Soong emits.
// 3. soong_ui executes .bootstrap/build.ninja
//
// (After this, Kati is executed to parse the Makefiles, but that's not part of
// bootstrapping Soong)

// A tiny struct used to tell Blueprint that it's in bootstrap mode. It would
// probably be nicer to use a flag in bootstrap.Args instead.
type BlueprintConfig struct {
	toolDir                   string
	soongOutDir               string
	outDir                    string
	runGoTests                bool
	debugCompilation          bool
	subninjas                 []string
	primaryBuilderInvocations []bootstrap.PrimaryBuilderInvocation
	isActionSandboxedBuild    bool
}

func (c BlueprintConfig) HostToolDir() string {
	return c.toolDir
}

func (c BlueprintConfig) SoongOutDir() string {
	return c.soongOutDir
}

func (c BlueprintConfig) OutDir() string {
	return c.outDir
}

func (c BlueprintConfig) RunGoTests() bool {
	return c.runGoTests
}

func (c BlueprintConfig) DebugCompilation() bool {
	return c.debugCompilation
}

func (c BlueprintConfig) Subninjas() []string {
	return c.subninjas
}

func (c BlueprintConfig) PrimaryBuilderInvocations() []bootstrap.PrimaryBuilderInvocation {
	return c.primaryBuilderInvocations
}

func (c BlueprintConfig) IsBootstrap() bool {
	return true
}

func (c BlueprintConfig) IsActionSandboxedBuild() bool {
	return c.isActionSandboxedBuild
}

func (c BlueprintConfig) ActionSandboxMetrics() *blueprint.SandboxMetrics {
	return nil
}

func environmentArgs(config Config, tag string) []string {
	return []string{
		"--available_env", shared.JoinPath(config.SoongOutDir(), availableEnvFile),
		"--used_env", config.UsedEnvFile(tag),
	}
}

func writeEmptyFile(ctx Context, path string) {
	err := os.MkdirAll(filepath.Dir(path), 0777)
	if err != nil {
		ctx.Fatalf("Failed to create parent directories of empty file '%s': %s", path, err)
	}

	if exists, err := fileExists(path); err != nil {
		ctx.Fatalf("Failed to check if file '%s' exists: %s", path, err)
	} else if !exists {
		err = os.WriteFile(path, nil, 0666)
		if err != nil {
			ctx.Fatalf("Failed to create empty file '%s': %s", path, err)
		}
	}
}

func fileExists(path string) (bool, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	return true, nil
}

type PrimaryBuilderFactory struct {
	name         string
	description  string
	config       Config
	output       string
	specificArgs []string
	debugPort    string
}

func (pb PrimaryBuilderFactory) primaryBuilderInvocation(config Config) bootstrap.PrimaryBuilderInvocation {
	commonArgs := make([]string, 0, 0)

	commonArgs = append(commonArgs, "--kati_suffix", config.KatiSuffix())
	if !config.SkipKati() {
		commonArgs = append(commonArgs, "--kati_enabled")
	}

	if !pb.config.skipSoongTests {
		commonArgs = append(commonArgs, "-t")
	}

	if pb.config.buildFromSourceStub {
		commonArgs = append(commonArgs, "--build-from-source-stub")
	}

	if pb.config.moduleDebugFile != "" {
		commonArgs = append(commonArgs, "--soong_module_debug")
		commonArgs = append(commonArgs, pb.config.moduleDebugFile)
	}

	if pb.config.incrementalDebugFile != "" {
		commonArgs = append(commonArgs, "--incremental-debug-file")
		commonArgs = append(commonArgs, pb.config.incrementalDebugFile)
	}

	commonArgs = append(commonArgs, "-l", filepath.Join(pb.config.FileListDir(), "Android.bp.list"))
	invocationEnv := make(map[string]string)
	if pb.debugPort != "" {
		//debug mode
		commonArgs = append(commonArgs, "--delve_listen", pb.debugPort,
			"--delve_path", shared.ResolveDelveBinary())
		// GODEBUG=asyncpreemptoff=1 disables the preemption of goroutines. This
		// is useful because the preemption happens by sending SIGURG to the OS
		// thread hosting the goroutine in question and each signal results in
		// work that needs to be done by Delve; it uses ptrace to debug the Go
		// process and the tracer process must deal with every signal (it is not
		// possible to selectively ignore SIGURG). This makes debugging slower,
		// sometimes by an order of magnitude depending on luck.
		// The original reason for adding async preemption to Go is here:
		// https://github.com/golang/proposal/blob/master/design/24543-non-cooperative-preemption.md
		invocationEnv["GODEBUG"] = "asyncpreemptoff=1"
	}

	var allArgs []string
	allArgs = append(allArgs, pb.specificArgs...)

	allArgs = append(allArgs, commonArgs...)
	allArgs = append(allArgs, environmentArgs(pb.config, pb.name)...)
	if profileCpu := os.Getenv("SOONG_PROFILE_CPU"); profileCpu != "" {
		allArgs = append(allArgs, "--cpuprofile", profileCpu+"."+pb.name)
	}
	if profileMem := os.Getenv("SOONG_PROFILE_MEM"); profileMem != "" {
		allArgs = append(allArgs, "--memprofile", profileMem+"."+pb.name)
	}
	allArgs = append(allArgs, "Android.bp")

	return bootstrap.PrimaryBuilderInvocation{
		Implicits:   []string{pb.output + ".glob_results"},
		Outputs:     []string{pb.output},
		Args:        allArgs,
		Description: pb.description,
		// NB: Changing the value of this environment variable will not result in a
		// rebuild. The bootstrap Ninja file will change, but apparently Ninja does
		// not consider changing the pool specified in a statement a change that's
		// worth rebuilding for.
		Console: os.Getenv("SOONG_UNBUFFERED_OUTPUT") == "1",
		Env:     invocationEnv,
	}
}

// bootstrapEpochCleanup deletes files used by bootstrap during incremental builds across
// incompatible changes.  Incompatible changes are marked by incrementing the bootstrapEpoch
// constant.  A tree is considered out of date for the current epoch of the
// .soong.bootstrap.epoch.<epoch> file doesn't exist.
func bootstrapEpochCleanup(ctx Context, config Config) {
	epochFile := fmt.Sprintf(".soong.bootstrap.epoch.%d", bootstrapEpoch)
	epochPath := filepath.Join(config.SoongOutDir(), epochFile)
	if exists, err := fileExists(epochPath); err != nil {
		ctx.Fatalf("failed to check if bootstrap epoch file %q exists: %q", epochPath, err)
	} else if !exists {
		// The tree is out of date for the current epoch, delete files used by bootstrap
		// and force the primary builder to rerun.
		soongNinjaFile := config.SoongNinjaFile()
		os.Remove(soongNinjaFile)
		for _, file := range blueprint.GetNinjaShardFiles(soongNinjaFile) {
			if ok, _ := fileExists(file); ok {
				os.Remove(file)
			}
		}
		os.Remove(soongNinjaFile + ".globs")
		os.Remove(soongNinjaFile + ".globs_time")
		os.Remove(soongNinjaFile + ".glob_results")

		// Mark the tree as up to date with the current epoch by writing the epoch marker file.
		writeEmptyFile(ctx, epochPath)
	}
}

func bootstrapBlueprint(ctx Context, config Config) {
	e := ctx.BeginTrace(metrics.RunSoong, "blueprint bootstrap")
	defer e.End()

	st := ctx.Status.StartTool()
	defer st.Finish()
	st.SetTotalActions(1)
	action := &status.Action{
		Description: "bootstrap blueprint",
		Outputs:     []string{"bootstrap blueprint"},
	}
	st.StartAction(action)

	// Clean up some files for incremental builds across incompatible changes.
	bootstrapEpochCleanup(ctx, config)

	baseArgs := []string{"--soong_variables", config.SoongVarsFile()}

	mainSoongBuildExtraArgs := append(baseArgs, "-o", config.SoongNinjaFile())
	analysisStatusFile := soongAnalysisStatusFile(config)
	mainSoongBuildExtraArgs = append(mainSoongBuildExtraArgs, "--status-file", analysisStatusFile)
	if config.EmptyNinjaFile() {
		mainSoongBuildExtraArgs = append(mainSoongBuildExtraArgs, "--empty-ninja-file")
	}
	if config.buildFromSourceStub {
		mainSoongBuildExtraArgs = append(mainSoongBuildExtraArgs, "--build-from-source-stub")
	}
	if config.ensureAllowlistIntegrity {
		mainSoongBuildExtraArgs = append(mainSoongBuildExtraArgs, "--ensure-allowlist-integrity")
	}
	if config.incrementalBuildActions {
		mainSoongBuildExtraArgs = append(mainSoongBuildExtraArgs, "--incremental-build-actions")
	}
	if len(config.partialAnalysisTargets) > 0 {
		mainSoongBuildExtraArgs = append(mainSoongBuildExtraArgs, fmt.Sprintf("%s=\"%s\"", "--partial-analysis-targets", config.partialAnalysisTargets))
	}
	if config.incrementalProviderTest {
		mainSoongBuildExtraArgs = append(mainSoongBuildExtraArgs, "--incremental-provider-test")
	}

	pbfs := []PrimaryBuilderFactory{
		{
			name:         soongBuildTag,
			description:  fmt.Sprintf("analyzing Android.bp files and generating ninja file at %s", config.SoongNinjaFile()),
			config:       config,
			output:       config.SoongNinjaFile(),
			specificArgs: mainSoongBuildExtraArgs,
		},
		{
			name:        jsonModuleGraphTag,
			description: fmt.Sprintf("generating the Soong module graph at %s", config.ModuleGraphFile()),
			config:      config,
			output:      config.ModuleGraphFile(),
			specificArgs: append(baseArgs,
				"--module_graph_file", config.ModuleGraphFile(),
				"--module_actions_file", config.ModuleActionsFile(),
			),
		},
		{
			name:        soongDocsTag,
			description: fmt.Sprintf("generating Soong docs at %s", config.SoongDocsHtml()),
			config:      config,
			output:      config.SoongDocsHtml(),
			specificArgs: append(baseArgs,
				"--soong_docs", config.SoongDocsHtml(),
			),
		},
	}

	// Figure out which invocations will be run under the debugger:
	//   * SOONG_DELVE if set specifies listening port
	//   * SOONG_DELVE_STEPS if set specifies specific invocations to be debugged, otherwise all are
	debuggedInvocations := make(map[string]bool)
	delvePort := os.Getenv("SOONG_DELVE")
	if delvePort != "" {
		if steps := os.Getenv("SOONG_DELVE_STEPS"); steps != "" {
			var validSteps []string
			for _, pbf := range pbfs {
				debuggedInvocations[pbf.name] = false
				validSteps = append(validSteps, pbf.name)

			}
			for _, step := range strings.Split(steps, ",") {
				if _, ok := debuggedInvocations[step]; ok {
					debuggedInvocations[step] = true
				} else {
					ctx.Fatalf("SOONG_DELVE_STEPS contains unknown soong_build step %s\n"+
						"Valid steps are %v", step, validSteps)
				}
			}
		} else {
			//  SOONG_DELVE_STEPS is not set, run all steps in the debugger
			for _, pbf := range pbfs {
				debuggedInvocations[pbf.name] = true
			}
		}
	}

	var invocations []bootstrap.PrimaryBuilderInvocation
	for _, pbf := range pbfs {
		if debuggedInvocations[pbf.name] {
			pbf.debugPort = delvePort
		}
		pbi := pbf.primaryBuilderInvocation(config)
		invocations = append(invocations, pbi)
	}

	blueprintArgs := bootstrap.Args{
		ModuleListFile: filepath.Join(config.FileListDir(), "Android.bp.list"),
		OutFile:        shared.JoinPath(config.SoongOutDir(), "bootstrap.ninja"),
		EmptyNinjaFile: false,
	}

	blueprintCtx := blueprint.NewContext()
	blueprintCtx.AddSourceRootDirs(config.GetSourceRootDirs()...)
	blueprintCtx.SetIgnoreUnknownModuleTypes(true)
	blueprintConfig := BlueprintConfig{
		soongOutDir: config.SoongOutDir(),
		toolDir:     config.HostToolDir(),
		outDir:      config.OutDir(),
		runGoTests:  !config.skipSoongTests,
		// If we want to debug soong_build, we need to compile it for debugging
		debugCompilation:          delvePort != "",
		primaryBuilderInvocations: invocations,
		isActionSandboxedBuild:    config.IsActionSandboxedBuild(),
	}

	// since `bootstrap.ninja` is regenerated unconditionally, we ignore the deps, i.e. little
	// reason to write a `bootstrap.ninja.d` file
	_, err := bootstrap.RunBlueprint(blueprintArgs, bootstrap.DoEverything, blueprintCtx, blueprintConfig)

	result := status.ActionResult{
		Action: action,
	}
	if err != nil {
		result.Error = err
		result.Output = err.Error()
	}
	st.FinishAction(result)
	if err != nil {
		ctx.Fatalf("bootstrap failed")
	}
}

func checkEnvironmentFile(ctx Context, config Config, currentEnv *Environment, envFile string) {
	getenv := func(k string) string {
		v, _ := currentEnv.Get(k)
		return v
	}

	// Log the changed environment variables to ChangedEnvironmentVariable field

	if stale, changedEnvironmentVariableList, err := shared.StaleEnvFile(envFile, getenv); stale {
		if config.EnforceNoReanalysis() {
			if exists, _ := fileExists(envFile); exists {
				if err != nil {
					msg := fmt.Sprintf("Reanalysis will run due to missing or invalid environment file: %s. Changed TARGET_PRODUCT?", err)
					// Both Error and Fatalf are used because Fatalf cannot write the error message to
					// error.log.
					ctx.Status.Error(msg)
					ctx.Fatalf(msg)
				}
				msg := fmt.Sprintf("Reanalysis will run due to environment change. Changed environment variables: %v", changedEnvironmentVariableList)
				// Both Error and Fatalf are used because Fatalf cannot write the error message to
				// error.log.
				ctx.Status.Error(msg)
				ctx.Fatalf(msg)
			}
		}
		for _, changedEnvironmentVariable := range changedEnvironmentVariableList {
			ctx.Metrics.AddChangedEnvironmentVariable(changedEnvironmentVariable)
		}
		os.Remove(envFile)
	}
}

func updateSymlinks(ctx Context, dir, prevCWD, cwd string, updateSemaphore chan struct{}) error {
	defer symlinkWg.Done()

	visit := func(path string, d fs.DirEntry, err error) error {
		if d.IsDir() && path != dir {
			symlinkWg.Add(1)
			go updateSymlinks(ctx, path, prevCWD, cwd, updateSemaphore)
			return filepath.SkipDir
		}
		f, err := d.Info()
		if err != nil {
			return err
		}
		// If the file is not a symlink, we don't have to update it.
		if f.Mode()&os.ModeSymlink != os.ModeSymlink {
			return nil
		}

		atomic.AddUint32(&numFound, 1)
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		if strings.HasPrefix(target, prevCWD) &&
			(len(target) == len(prevCWD) || target[len(prevCWD)] == '/') {
			target = filepath.Join(cwd, target[len(prevCWD):])
			if err := os.Remove(path); err != nil {
				return err
			}
			if err := os.Symlink(target, path); err != nil {
				return err
			}
			atomic.AddUint32(&numUpdated, 1)
		}
		return nil
	}

	<-updateSemaphore
	defer func() { updateSemaphore <- struct{}{} }()
	if err := filepath.WalkDir(dir, visit); err != nil {
		return err
	}
	return nil
}

// b/376466642: If the concurrency of updateSymlinks is unbounded, Go's runtime spawns a
// theoretically unbounded number of threads to handle blocking syscalls. This causes the runtime to
// panic due to hitting thread limits in rare cases. Limiting to GOMAXPROCS concurrent symlink
// updates should make this a non-issue.
func newUpdateSemaphore() chan struct{} {
	numPermits := runtime.GOMAXPROCS(0)
	c := make(chan struct{}, numPermits)
	for i := 0; i < numPermits; i++ {
		c <- struct{}{}
	}
	return c
}

func fixOutDirSymlinks(ctx Context, config Config, outDir string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	// Record the .top as the very last thing in the function.
	tf := filepath.Join(outDir, ".top")
	defer func() {
		if err := os.WriteFile(tf, []byte(cwd), 0644); err != nil {
			fmt.Fprintf(os.Stderr, "Unable to log CWD: %v", err)
		}
	}()

	// Find the previous working directory if it was recorded.
	var prevCWD string
	pcwd, err := os.ReadFile(tf)
	if err != nil {
		if os.IsNotExist(err) {
			// No previous working directory recorded, nothing to do.
			return nil
		}
		ctx.Println(fmt.Sprintf("Failed to read pcwd: %v", err))
		return err
	}

	prevCWD = strings.Trim(string(pcwd), "\n")

	if prevCWD == cwd || prevCWD == "" {
		// We are in the same source dir, or prevCWD came up empty for some reason,
		// so nothing to update.
		return nil
	}

	ctx.Println(fmt.Sprintf("CWD directory changed from %v to %v, updating output symlinks", prevCWD, cwd))

	symlinkWg.Add(1)
	if err := updateSymlinks(ctx, outDir, prevCWD, cwd, newUpdateSemaphore()); err != nil {
		return err
	}
	symlinkWg.Wait()
	ctx.Println(fmt.Sprintf("Updated %d/%d symlinks in dir %v", numUpdated, numFound, outDir))

	return nil
}

func migrateOutputSymlinks(ctx Context, config Config) error {
	// Figure out the real out directory ("out" could be a symlink).
	outDir := config.OutDir()
	s, err := os.Lstat(outDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No out dir exists, no symlinks to migrate.
			return nil
		}
		return err
	}
	if s.Mode()&os.ModeSymlink == os.ModeSymlink {
		target, err := filepath.EvalSymlinks(outDir)
		if err != nil {
			return err
		}
		outDir = target
	}
	return fixOutDirSymlinks(ctx, config, outDir)
}

func runSoong(ctx Context, config Config, enforceNoSoongOutput bool) {
	e := ctx.BeginTrace(metrics.RunSoong, "soong")
	defer e.End()

	if err := migrateOutputSymlinks(ctx, config); err != nil {
		ctx.Fatalf("failed to migrate output directory to current TOP dir: %v", err)
	}

	// We have two environment files: .available is the one with every variable,
	// .used with the ones that were actually used. The latter is used to
	// determine whether Soong needs to be re-run since why re-run it if only
	// unused variables were changed?
	envFile := filepath.Join(config.SoongOutDir(), availableEnvFile)

	// This is done unconditionally, but does not take a measurable amount of time
	bootstrapBlueprint(ctx, config)

	soongBuildEnv := config.Environment().Copy()
	soongBuildEnv.Set("TOP", os.Getenv("TOP"))
	soongBuildEnv.Set("LOG_DIR", config.LogsDir())

	// For Soong bootstrapping tests
	if os.Getenv("ALLOW_MISSING_DEPENDENCIES") == "true" {
		soongBuildEnv.Set("ALLOW_MISSING_DEPENDENCIES", "true")
	}

	// Only inject the SOONG_ENFORCE_NO_REANALYSIS variable if it's not a clean build.
	// Clean build should not fail even if SOONG_ENFORCE_NO_REANALYSIS is set.
	cleanBuild := false
	if exists, _ := fileExists(config.UsedEnvFile(soongBuildTag)); !exists {
		cleanBuild = true
	}

	if config.EnforceNoReanalysis() && !cleanBuild {
		soongBuildEnv.Set("SOONG_ENFORCE_NO_REANALYSIS", "true")
	} else {
		soongBuildEnv.Unset("SOONG_ENFORCE_NO_REANALYSIS")
	}

	err := writeEnvironmentFile(ctx, envFile, soongBuildEnv.AsMap())
	if err != nil {
		ctx.Fatalf("failed to write environment file %s: %s", envFile, err)
	}

	func() {
		e := ctx.BeginTrace(metrics.RunSoong, "environment check")
		defer e.End()

		checkEnvironmentFile(ctx, config, soongBuildEnv, config.UsedEnvFile(soongBuildTag))

		if config.JsonModuleGraph() {
			checkEnvironmentFile(ctx, config, soongBuildEnv, config.UsedEnvFile(jsonModuleGraphTag))
		}

		if config.SoongDocs() {
			checkEnvironmentFile(ctx, config, soongBuildEnv, config.UsedEnvFile(soongDocsTag))
		}
	}()

	ninja := func(targets ...string) {
		e := ctx.BeginTrace(metrics.RunSoong, "bootstrap")
		defer e.End()

		fifo := filepath.Join(config.OutDir(), ".ninja_fifo")
		bootstrapStatus := newSoongBootstrapStatus(ctx.Status.StartTool(), config.SoongNinjaFile())
		nr := status.NewNinjaReader(ctx, bootstrapStatus, fifo, ctx.SigNumFunc)
		stopAnalysisStatus := monitorSoongAnalysisStatus(ctx, soongAnalysisStatusFile(config))
		defer stopAnalysisStatus()
		func() {
			defer nr.Close()
			var ninjaEnv Environment
			var ninjaCmd string
			var ninjaArgs []string
			switch config.ninjaCommand {
			case NINJA_N2:
				ninjaCmd = config.N2Bin()
				ninjaArgs = []string{
					// TODO: implement these features, or remove them.
					//"-d", "keepdepfile",
					//"-d", "stats",
					//"-o", "usesphonyoutputs=yes",
					//"-o", "preremoveoutputs=yes",
					//"-w", "dupbuild=err",
					//"-w", "outputdir=err",
					//"-w", "missingoutfile=err",
					"-v",
					"-j", strconv.Itoa(config.Parallel()),
					"--frontend-file", fifo,
					"-f", filepath.Join(config.SoongOutDir(), "bootstrap.ninja"),
				}
			case NINJA_SISO:
				sisoLogDir := filepath.Join(config.LogsDir(), "bootstrap")
				// create log dir, otherwise glog will use /tmp instead.
				err := os.MkdirAll(sisoLogDir, 0777)
				if err != nil {
					ctx.Fatalf("Failed to create siso log dir: %v\n", err)
				}
				ninjaCmd = config.SisoBin()
				ninjaArgs = []string{
					"--log_dir", sisoLogDir, // for glog, e.g. siso.*INFO*
					"--stderrthreshold", "3", // Keep Siso diagnostics in log files; action failures use the frontend.
					"ninja",
					"-d", "keepdepfile",
					// TODO: implement these features, or remove them.
					//"-d", "stats",
					//"-o", "usesphonyoutputs=yes",
					//"-o", "preremoveoutputs=yes",
					//"-w", "dupbuild=err",
					//"-w", "outputdir=err",
					//"-w", "missingoutfile=err",
					"--local_jobs", strconv.Itoa(config.Parallel()),
					//"--remote_jobs", strconv.Itoa(config.RemoteParallel()),
					"--frontend_file", fifo,
					"-f", filepath.Join(config.SoongOutDir(), "bootstrap.ninja"),
				}
				if value := config.SisoConfigDir(); value != "" {
					value = createSisoConfigDir(ctx, config, value)
					ninjaArgs = append(ninjaArgs, fmt.Sprintf("--config_repo_dir=%s", value))
				}
				sisoExperiments := []string{
					"ignore-missing-out-in-depfile",
					"fallback-on-exec-error",
				}
				if exps, ok := ninjaEnv.Get("SISO_EXPERIMENTS"); ok {
					sisoExperiments = append(sisoExperiments, exps)
				}
				ninjaEnv.Set("SISO_EXPERIMENTS", strings.Join(sisoExperiments, ","))

				if config.IsVerbose() {
					// Output `siso version`.
					vcmd := Command(ctx, config, nil, "siso version",
						config.SisoBin(), "version")
					versionOutput, err := vcmd.CombinedOutput()
					if err != nil {
						ctx.Fatalf("Failed to run siso version: %s\n", err)
					}
					ctx.Verbosef("%s", versionOutput)
				}
			default:
				// NINJA_NINJA is the default.
				ninjaCmd = config.NinjaBin()
				ninjaArgs = []string{
					"-d", "keepdepfile",
					"-d", "stats",
					"-o", "usesphonyoutputs=yes",
					"-o", "preremoveoutputs=yes",
					"-w", "dupbuild=err",
					"-w", "outputdir=err",
					"-w", "missingoutfile=err",
					"-j", strconv.Itoa(config.Parallel()),
					"--frontend_file", fifo,
					"-f", filepath.Join(config.SoongOutDir(), "bootstrap.ninja"),
				}
			}

			if extra, ok := config.Environment().Get("SOONG_UI_NINJA_ARGS"); ok {
				ctx.Printf(`CAUTION: arguments in $SOONG_UI_NINJA_ARGS=%q, e.g. "-n", can make soong_build FAIL or INCORRECT`, extra)
				ninjaArgs = append(ninjaArgs, strings.Fields(extra)...)
			}

			ninjaArgs = append(ninjaArgs, targets...)

			cmd := Command(ctx, config, e, "soong bootstrap",
				ninjaCmd, ninjaArgs...)

			// This is currently how the command line to invoke soong_build finds the
			// root of the source tree and the output root
			ninjaEnv.Set("TOP", os.Getenv("TOP"))
			SetupLitePath(ctx, config, "")
			ninjaPath, _ := config.Environment().Get("PATH")
			ninjaEnv.Set("PATH", ninjaPath)

			cmd.Environment = &ninjaEnv
			cmd.Sandbox = soongSandbox
			cmd.RunAndStreamOrFatal()
		}()

		if enforceNoSoongOutput && nr.HasAnyOutput() {
			ctx.Fatalf("Soong must not output anything to stdout/stderr on a successful build, please remove the prints")
		}
	}

	targets := make([]string, 0, 0)

	if config.JsonModuleGraph() {
		targets = append(targets, config.ModuleGraphFile())
	}

	if config.SoongDocs() {
		targets = append(targets, config.SoongDocsHtml())
	}

	if config.SoongBuildInvocationNeeded() {
		// This build generates <builddir>/build.ninja, which is used later by build/soong/ui/build/build.go#Build().
		targets = append(targets, config.SoongNinjaFile())
	}

	for _, target := range targets {
		if err := checkGlobs(ctx, config, target); err != nil {
			ctx.Fatalf("Error checking globs: %s", err.Error())
		}
	}

	beforeSoongTimestamp := time.Now()

	ninja(targets...)

	loadSoongBuildMetrics(ctx, config, beforeSoongTimestamp)

	soongNinjaFile := config.SoongNinjaFile()
	distGzipFile(ctx, config, soongNinjaFile, "soong_ui/soong")
	for _, file := range blueprint.GetNinjaShardFiles(soongNinjaFile) {
		if ok, _ := fileExists(file); ok {
			distGzipFile(ctx, config, file, "soong_ui/soong")
		}
	}
	distFile(ctx, config, config.SoongVarsFile(), "soong_ui/soong")
	distFile(ctx, config, config.SoongExtraVarsFile(), "soong_ui/soong")

	if !config.SkipKati() {
		distGzipFile(ctx, config, config.SoongAndroidMk(), "soong_ui/soong")
		distGzipFile(ctx, config, config.SoongMakeVarsMk(), "soong_ui/soong")
	} else {
		distGzipFile(ctx, config, config.SoongPhonyNinjaFile(), "soong_ui/soong")
		distGzipFile(ctx, config, config.SoongDistNinjaFile(), "soong_ui/soong")
		distGzipFile(ctx, config, config.SoongNoDistNinjaFile(), "soong_ui/soong")
	}

	if config.JsonModuleGraph() {
		distGzipFile(ctx, config, config.ModuleGraphFile(), "soong_ui/soong")
	}

	if config.ninjaCommand == NINJA_SISO {
		distFile(ctx, config, config.SisoConfigFile(true), "soong_ui/bootstrap")
		distFile(ctx, config, config.SisoDepsFile(true), "soong_ui/bootstrap")
		distFile(ctx, config, config.SisoFsStateFile(true), "soong_ui/bootstrap")
		distFile(ctx, config, config.SisoFilegroupsFile(true), "soong_ui/bootstrap")
	}
}

// soongBootstrapStatus replaces the single primary soong_build Ninja edge with
// the detailed analysis actions reported through monitorSoongAnalysisStatus.
// Other bootstrap edges, such as rebuilding soong_build itself, remain visible.
type soongBootstrapStatus struct {
	status.ToolStatus
	soongNinjaFile string
	totalActions   int
	suppressed     map[*status.Action]bool
	mutex          sync.Mutex
}

func newSoongBootstrapStatus(tool status.ToolStatus, soongNinjaFile string) *soongBootstrapStatus {
	return &soongBootstrapStatus{
		ToolStatus:     tool,
		soongNinjaFile: soongNinjaFile,
		suppressed:     make(map[*status.Action]bool),
	}
}

func (s *soongBootstrapStatus) SetTotalActions(total int) {
	s.mutex.Lock()
	s.totalActions = total
	suppressed := len(s.suppressed)
	s.mutex.Unlock()
	s.ToolStatus.SetTotalActions(max(0, total-suppressed))
}

func (s *soongBootstrapStatus) StartAction(action *status.Action) {
	if slices.Contains(action.Outputs, s.soongNinjaFile) {
		s.mutex.Lock()
		s.suppressed[action] = true
		total := s.totalActions
		suppressed := len(s.suppressed)
		s.mutex.Unlock()
		s.ToolStatus.SetTotalActions(max(0, total-suppressed))
		return
	}
	s.ToolStatus.StartAction(action)
}

func (s *soongBootstrapStatus) FinishAction(result status.ActionResult) {
	s.mutex.Lock()
	suppressed := s.suppressed[result.Action]
	if suppressed {
		delete(s.suppressed, result.Action)
	}
	s.mutex.Unlock()
	if !suppressed {
		s.ToolStatus.FinishAction(result)
		return
	}
	if result.Error != nil {
		// Restore failed actions so their command output remains visible.
		s.ToolStatus.StartAction(result.Action)
		s.ToolStatus.FinishAction(result)
	}
}

// checkGlobs manages the globs that cause soong to rerun.
//
// When soong_build runs, it will run globs. It will write all the globs
// it ran into the "{finalOutFile}.globs" file. Then every build,
// soong_ui will check that file, rerun the globs, and if they changed
// from the results that soong_build got, update the ".glob_results"
// file, causing soong_build to rerun. The ".glob_results" file will
// be empty on the first run of soong_build, because we don't know
// what the globs are yet, but also remain empty until the globs change
// so that we don't run soong_build a second time unnecessarily.
// Both soong_build and soong_ui will also update a ".globs_time" file
// with the time that they ran at every build. When soong_ui checks
// globs, it only reruns globs whose dependencies are newer than the
// time in the ".globs_time" file.
func checkGlobs(ctx Context, config Config, finalOutFile string) error {
	e := ctx.BeginTrace(metrics.RunSoong, "check_globs")
	defer e.End()
	st := ctx.Status.StartTool()
	st.Status("Running globs...")
	defer st.Finish()

	globsFile, err := os.Open(finalOutFile + ".globs")
	if errors.Is(err, os.ErrNotExist) {
		// if the glob file doesn't exist, make sure the glob_results file exists and is empty.
		if err := os.MkdirAll(filepath.Dir(finalOutFile), 0777); err != nil {
			return err
		}
		return os.WriteFile(finalOutFile+".glob_results", nil, 0666)
	} else if err != nil {
		return err
	}
	defer globsFile.Close()

	globsTimeBytes, err := os.ReadFile(finalOutFile + ".globs_time")
	if errors.Is(err, os.ErrNotExist) {
		globsTimeBytes = []byte("0")
	} else if err != nil {
		return err
	}

	globsTimeMicros, err := strconv.ParseInt(strings.TrimSpace(string(globsTimeBytes)), 10, 64)
	if err != nil {
		return err
	}
	globCheckStartTime := time.Now().UnixMicro()

	hasChangedGlobs, err := pathtools.CheckForChangedGlobs(pathtools.OsFs, globsFile, globsTimeMicros)
	if err != nil {
		if config.EnforceNoReanalysis() {
			msg := fmt.Sprintf("Reanalysis will run due to glob error: %s", err.Error())
			// Both Error and Fatalf are used because Fatalf cannot write the error message to
			// error.log.
			ctx.Status.Error(msg)
			ctx.Fatalf(msg)
		}
		fmt.Fprintf(os.Stdout, "\nGlobs changed, rerunning soong...\n")
		fmt.Fprintf(os.Stdout, "%s\n", err.Error())
	}

	if hasChangedGlobs {
		// Write the current time to the glob_results file. We just need
		// some unique value to trigger a rerun, it doesn't matter what it is.
		err = os.WriteFile(
			finalOutFile+".glob_results",
			[]byte(fmt.Sprintf("%d\n", globCheckStartTime)),
			0666,
		)
		if err != nil {
			return err
		}
	} else {
		// Update the globs_time file if there were no changed globs so that we don't
		// rerun globs in the future that we just saw didn't change.
		err = os.WriteFile(
			finalOutFile+".globs_time",
			[]byte(fmt.Sprintf("%d\n", globCheckStartTime)),
			0666,
		)
		if err != nil {
			return err
		}
	}
	return nil
}

func monitorSoongAnalysisStatus(ctx Context, statusFile string) func() {
	_ = os.Remove(statusFile)
	tool := ctx.Status.StartTool()
	tool.SetTotalActions(11)
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)

	var currentAction *status.Action
	processedBytes := 0
	bucketTotal := 0
	bucketActions := make(map[int]*status.Action)
	startAction := func(message string) {
		if currentAction != nil {
			tool.FinishAction(status.ActionResult{Action: currentAction})
		}
		currentAction = &status.Action{
			Description: message,
			Outputs:     []string{statusFile},
		}
		tool.StartAction(currentAction)
	}
	processLine := func(line string) {
		fields := strings.Split(line, "\t")
		if len(fields) == 2 && fields[0] == "stage" {
			if fields[1] == "Writing module Ninja buckets..." {
				return
			}
			if currentAction != nil {
				tool.FinishAction(status.ActionResult{Action: currentAction})
				currentAction = nil
			}
			for bucket, action := range bucketActions {
				tool.FinishAction(status.ActionResult{Action: action})
				delete(bucketActions, bucket)
			}
			if fields[1] != "Android.bp analysis complete" {
				startAction(fields[1])
			}
			return
		}
		if len(fields) != 4 || fields[0] != "progress" {
			return
		}
		value, valueErr := strconv.Atoi(fields[2])
		total, totalErr := strconv.Atoi(fields[3])
		if valueErr != nil || totalErr != nil || value < 0 || total < 0 {
			return
		}
		switch fields[1] {
		case "ninja_buckets":
			if value != 0 {
				return
			}
			bucketTotal = total
			if currentAction != nil {
				tool.FinishAction(status.ActionResult{Action: currentAction})
				currentAction = nil
			}
			if total == 0 {
				tool.SetTotalActions(11)
				startAction("No module Ninja buckets need updating")
			} else {
				// Replace the generic bucket-writing stage with one action for
				// every dirty bucket. Up to eight run concurrently.
				tool.SetTotalActions(10 + total)
			}
		case "ninja_bucket_start":
			if total != bucketTotal || total == 0 || value == 0 || value > total {
				return
			}
			if _, exists := bucketActions[value]; exists {
				return
			}
			action := &status.Action{
				Description: fmt.Sprintf("Writing module Ninja bucket %d/%d...", value, total),
				Outputs:     []string{statusFile},
			}
			bucketActions[value] = action
			tool.StartAction(action)
		case "ninja_bucket_finish":
			if total != bucketTotal {
				return
			}
			if action, exists := bucketActions[value]; exists {
				tool.FinishAction(status.ActionResult{Action: action})
				delete(bucketActions, value)
			}
		}
	}
	readStatus := func() {
		data, err := os.ReadFile(statusFile)
		if err != nil {
			return
		}
		if processedBytes >= len(data) {
			return
		}
		newData := data[processedBytes:]
		lastNewline := bytes.LastIndexByte(newData, '\n')
		if lastNewline < 0 {
			return
		}
		for _, line := range strings.Split(string(newData[:lastNewline]), "\n") {
			if line != "" {
				processLine(line)
			}
		}
		processedBytes += lastNewline + 1
	}

	go func() {
		defer wg.Done()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				readStatus()
			case <-done:
				readStatus()
				return
			}
		}
	}()

	return func() {
		close(done)
		wg.Wait()
		if currentAction != nil {
			tool.FinishAction(status.ActionResult{Action: currentAction})
		}
		for _, action := range bucketActions {
			tool.FinishAction(status.ActionResult{Action: action})
		}
		tool.Finish()
		_ = os.Remove(statusFile)
	}
}

// loadSoongBuildMetrics reads out/soong_build_metrics.pb if it was generated by soong_build and copies the
// events stored in it into the soong_ui trace to provide introspection into how long the different phases of
// soong_build are taking.
func loadSoongBuildMetrics(ctx Context, config Config, oldTimestamp time.Time) {
	soongBuildMetricsFile := config.SoongBuildMetrics()
	if metricsStat, err := os.Stat(soongBuildMetricsFile); err != nil {
		ctx.Verbosef("Failed to stat %s: %s", soongBuildMetricsFile, err)
		return
	} else if !metricsStat.ModTime().After(oldTimestamp) {
		ctx.Verbosef("%s timestamp not later after running soong, expected %s > %s",
			soongBuildMetricsFile, metricsStat.ModTime(), oldTimestamp)
		return
	}

	metricsData, err := os.ReadFile(soongBuildMetricsFile)
	if err != nil {
		ctx.Verbosef("Failed to read %s: %s", soongBuildMetricsFile, err)
		return
	}

	soongBuildMetrics := metrics_proto.SoongBuildMetrics{}
	err = proto.Unmarshal(metricsData, &soongBuildMetrics)
	if err != nil {
		ctx.Verbosef("Failed to unmarshal %s: %s", soongBuildMetricsFile, err)
		return
	}
	for _, event := range soongBuildMetrics.Events {
		desc := event.GetDescription()
		if dot := strings.LastIndexByte(desc, '.'); dot >= 0 {
			desc = desc[dot+1:]
		}
		ctx.Tracer.Complete(desc, ctx.Thread,
			event.GetStartTime(), event.GetStartTime()+event.GetRealTime())
	}
	for _, event := range soongBuildMetrics.PerfCounters {
		timestamp := event.GetTime()
		for _, group := range event.Groups {
			counters := make([]tracer.Counter, 0, len(group.Counters))
			for _, counter := range group.Counters {
				counters = append(counters, tracer.Counter{
					Name:  counter.GetName(),
					Value: counter.GetValue(),
				})
			}
			ctx.Tracer.CountersAtTime(group.GetName(), ctx.Thread, timestamp, counters)
		}
	}
}
