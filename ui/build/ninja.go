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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"android/soong/shared"
	"android/soong/ui/metrics"
	"android/soong/ui/status"
)

const (
	// File containing the environment state when ninja is executed
	ninjaEnvFileName        = "ninja.environment"
	ninjaLogFileName        = ".ninja_log"
	ninjaWeightListFileName = ".ninja_weight_list"
)

// Runs ninja with the arguments from the command line, as found in
// config.NinjaArgs().
func runNinjaForBuild(ctx Context, config Config) {
	runNinja(ctx, config, config.NinjaArgs())
}

// Constructs and runs the Ninja command line with a restricted set of
// environment variables. It's important to restrict the environment Ninja runs
// for hermeticity reasons, and to avoid spurious rebuilds.
func runNinja(ctx Context, config Config, ninjaArgs []string) {
	e := ctx.BeginTrace(metrics.PrimaryNinja, "ninja")
	defer e.End()

	// Sets up the FIFO status updater that reads the Ninja protobuf output, and
	// translates it to the soong_ui status output, displaying real-time
	// progress of the build.
	fifo := filepath.Join(config.OutDir(), ".ninja_fifo")
	nr := status.NewNinjaReader(ctx, ctx.Status.StartTool(), fifo, ctx.SigNumFunc)
	defer nr.Close()

	var executable string
	var args []string
	var parallel int
	if config.UseRemoteBuild() {
		parallel = config.RemoteParallel()
	} else {
		parallel = config.Parallel()
	}

	sisoExperiments := []string{}
	switch config.ninjaCommand {
	case NINJA_N2:
		executable = config.N2Bin()
		args = []string{
			"-d", "trace",
			// TODO: implement these features, or remove them.
			//"-d", "keepdepfile",
			//"-d", "keeprsp",
			//"-d", "stats",
			"--frontend-file", fifo,
			"-j", strconv.Itoa(parallel),
		}
	case NINJA_SISO:
		executable = config.SisoBin()
		args = []string{
			"--log_dir", config.LogsDir(), // for glog, e.g. siso.*INFO*
			"--stderrthreshold", "3", // Keep Siso diagnostics in log files; action failures use the frontend.
			"ninja",
			// TODO: implement these features, or remove them.
			//"-d", "trace",
			"-d", "keepdepfile",
			"-d", "keeprsp",
			//"-d", "stats",
			"--frontend_file", fifo,
			"--local_jobs", strconv.Itoa(config.Parallel()),
			"--log_dir", config.LogsDir(),
		}
		if value := config.SisoConfigDir(); value != "" {
			value = createSisoConfigDir(ctx, config, value)
			args = append(args, fmt.Sprintf("--config_repo_dir=%s", value))
		}
		// b/374179435
		if config.BuildBrokenMissingOutputs() {
			// By default, Siso treats missing outputs as errors.
			sisoExperiments = append(sisoExperiments, "ignore-missing-outputs")
		}
		sisoExperiments = append(sisoExperiments,
			// b/430486641
			"ignore-missing-out-in-depfile",
			// b/479933778
			"allow-unexpected-rsp-remove",
			// set oom-score-adj=1000 on local action.
			"oom-score-adj",
		)
		var sisoConfigs []string
		switch {
		case config.StartReproxy():
			ctx.Verbosef("with reclient\n")
			sisoConfigs = append(sisoConfigs, "reclient") // not used in siso config star?
			if config.RemoteParallel() != 0 {
				args = append(args, "--remote_jobs", strconv.Itoa(config.RemoteParallel()))
			}
			// Explicitly turn off reapi in Siso.
			args = append(args, "--project=", "--reapi_address=", "--reapi_instance=")
		case config.UseRBEproxy():
			ctx.Verbosef("with rbeproxy\n")
			args = append(args, "--write_reclient_metrics_logs")
			if config.RemoteParallel() != 0 {
				args = append(args, "--remote_jobs", strconv.Itoa(config.RemoteParallel()))
			}
			if project := getRBEProject(ctx, config); project != "" {
				args = append(args, "--project", project)
			}
			if instance, ok := config.environ.Get("RBE_instance"); ok {
				args = append(args, "--reapi_instance", instance)
			}
			if service, ok := config.environ.Get("RBE_service"); ok {
				service = getRBEproxySocket(ctx, config)
				args = append(args, "--reapi_address", service)
			}
			args = append(args, "--reapi_insecure")
		default:
			ctx.Verbosef("local only\n")
		}
		// when action sandboxing enabled, sisoConfigVars
		// has nsjail_path, which passed as template vars
		// for main.star and add "sandbox" config in step_config.
		// we share siso config between bootstrap and ninja,
		// but enable sandbox only for ninja by
		// `--config action_sandbox`.
		if config.IsActionSandboxedBuild() {
			sisoConfigs = append(sisoConfigs, "action_sandbox")
		}
		if len(sisoConfigs) > 0 {
			args = append(args, "--config", strings.Join(sisoConfigs, ","))
		}
	default:
		// NINJA_NINJA or NINJA_NINJAGO.
		executable = config.NinjaBin()
		args = []string{
			"-d", "keepdepfile",
			"-d", "keeprsp",
			"-d", "stats",
			"--frontend_file", fifo,
			"-o", "usesphonyoutputs=yes",
			"-w", "dupbuild=err",
			"-w", "missingdepfile=err",
			"-j", strconv.Itoa(parallel),
		}
		// Missing outputs will be treated as errors.
		// BUILD_BROKEN_MISSING_OUTPUTS can be used to bypass this check.
		if !config.BuildBrokenMissingOutputs() {
			args = append(args,
				"-w", "missingoutfile=err",
			)
		}

		if config.IsActionSandboxedBuild() {
			ninjaArgs = append(ninjaArgs, []string{
				"-o", fmt.Sprintf("nsjail=%s", config.PrebuiltBuildTool("nsjail")),
				"-o", fmt.Sprintf("nsjail_workdir=%s", filepath.Join(config.SoongOutDir(), "action_sandboxing_workdir")),
			}...)
		}
	}
	args = append(args, ninjaArgs...)

	// TODO(jihoonkang): Remove this check once non-ninja executors start supporting action sandboxing
	if config.IsActionSandboxedBuild() {
		switch config.ninjaCommand {
		case NINJA_NINJA, NINJA_SISO:
		default:
			ctx.Fatalf("Action sandboxing is not supported for %s, set SOONG_NINJA=ninja or SOONG_NINJA=siso", config.ninjaCommand)
		}
	}

	if config.keepGoing != 1 {
		args = append(args, "-k", strconv.Itoa(config.keepGoing))
	}

	args = append(args, "-f", config.CombinedNinjaFile())

	cmd := Command(ctx, config, e, "ninja", executable, args...)

	// Set up the nsjail sandbox Ninja runs in.
	cmd.Sandbox = ninjaSandbox
	if config.HasKatiSuffix() {
		// Reads and executes a shell script from Kati that sets/unsets the
		// environment Ninja runs in.
		cmd.Environment.AppendFromKati(config.KatiEnvFile())
	}

	// TODO(b/346806126): implement this for the other ninjaCommand values.
	switch config.ninjaCommand {
	case NINJA_NINJA:
		switch config.NinjaWeightListSource() {
		case NINJA_LOG:
			cmd.Args = append(cmd.Args, "-o", "usesninjalogasweightlist=yes")
		case EVENLY_DISTRIBUTED:
			// pass empty weight list means ninja considers every tasks's weight as 1(default value).
			cmd.Args = append(cmd.Args, "-o", "usesweightlist=/dev/null")
		case EXTERNAL_FILE:
			fallthrough
		case HINT_FROM_SOONG:
			// The weight list is already copied/generated.
			ninjaWeightListPath := filepath.Join(config.OutDir(), ninjaWeightListFileName)
			cmd.Args = append(cmd.Args, "-o", "usesweightlist="+ninjaWeightListPath)
		}
	case NINJA_SISO:
		if expsValue, ok := cmd.Environment.Get("SISO_EXPERIMENTS"); ok {
			sisoExperiments = append(sisoExperiments, expsValue)
		}
		cmd.Environment.Set("SISO_EXPERIMENTS", strings.Join(sisoExperiments, ","))
	}

	// Allow both NINJA_ARGS and NINJA_EXTRA_ARGS, since both have been
	// used in the past to specify extra ninja arguments.
	if extra, ok := cmd.Environment.Get("NINJA_ARGS"); ok {
		cmd.Args = append(cmd.Args, strings.Fields(extra)...)
	}
	if extra, ok := cmd.Environment.Get("NINJA_EXTRA_ARGS"); ok {
		cmd.Args = append(cmd.Args, strings.Fields(extra)...)
	}

	ninjaHeartbeatDuration := time.Minute * 5
	// Get the ninja heartbeat interval from the environment before it's filtered away later.
	if overrideText, ok := cmd.Environment.Get("NINJA_HEARTBEAT_INTERVAL"); ok {
		// For example, "1m"
		overrideDuration, err := time.ParseDuration(overrideText)
		if err == nil && overrideDuration.Seconds() > 0 {
			ninjaHeartbeatDuration = overrideDuration
		}
	}

	// Filter the environment, as ninja does not rebuild files when environment
	// variables change.
	//
	// Anything listed here must not change the output of rules/actions when the
	// value changes, otherwise incremental builds may be unsafe. Vars
	// explicitly set to stable values elsewhere in soong_ui are fine.
	//
	// For the majority of cases, either Soong or the makefiles should be
	// replicating any necessary environment variables in the command line of
	// each action that needs it.
	if cmd.Environment.IsEnvTrue("ALLOW_NINJA_ENV") {
		ctx.Println("Allowing all environment variables during ninja; incremental builds may be unsafe.")
	} else {
		cmd.Environment.Allow(append([]string{
			// Set the path to a symbolizer (e.g. llvm-symbolizer) so ASAN-based
			// tools can symbolize crashes.
			"ASAN_SYMBOLIZER_PATH",
			"HOME",
			"JAVA_HOME",
			"LANG",
			"LC_MESSAGES",
			"OUT_DIR",
			"PATH",
			"PWD",
			// https://docs.python.org/3/using/cmdline.html#envvar-PYTHONDONTWRITEBYTECODE
			"PYTHONDONTWRITEBYTECODE",
			"TMPDIR",
			"USER",

			// TODO: remove these carefully
			// Options for the address sanitizer.
			"ASAN_OPTIONS",
			// The list of Android app modules to be built in an unbundled manner.
			"TARGET_BUILD_APPS",
			// The variant of the product being built. e.g. eng, userdebug, debug.
			"TARGET_BUILD_VARIANT",
			// The product name of the product being built, e.g. aosp_arm, aosp_flame.
			"TARGET_PRODUCT",
			// b/147197813 - used by art-check-debug-apex-gen
			"EMMA_INSTRUMENT_FRAMEWORK",

			// RBE client
			"RBE_compare",
			"RBE_num_local_reruns",
			"RBE_num_remote_reruns",
			"RBE_exec_root",
			"RBE_exec_strategy",
			"RBE_invocation_id",
			"RBE_log_dir",
			"RBE_num_retries_if_mismatched",
			"RBE_platform",
			"RBE_remote_accept_cache",
			"RBE_remote_update_cache",
			"RBE_server_address",
			// TODO: remove old FLAG_ variables.
			"FLAG_compare",
			"FLAG_exec_root",
			"FLAG_exec_strategy",
			"FLAG_invocation_id",
			"FLAG_log_dir",
			"FLAG_platform",
			"FLAG_remote_accept_cache",
			"FLAG_remote_update_cache",
			"FLAG_server_address",

			// ccache settings
			"CCACHE_COMPILERCHECK",
			"CCACHE_SLOPPINESS",
			"CCACHE_BASEDIR",
			"CCACHE_CPP2",
			"CCACHE_DIR",

			// LLVM compiler wrapper options
			"TOOLCHAIN_RUSAGE_OUTPUT",

			// We don't want this build broken flag to cause reanalysis, so allow it through to the
			// actions.
			"BUILD_BROKEN_INCORRECT_PARTITION_IMAGES",
			// Do not do reanalysis just because we changed ninja commands.
			"SOONG_NINJA",
			"RUST_BACKTRACE",
			"RUST_LOG",

			// Directory for ExecutionMetrics
			"SOONG_METRICS_AGGREGATION_DIR",

			// SISO experiments for bringup
			"SISO_EXPERIMENTS",

			// CIPD proxy
			"CIPD_PROXY_URL",

			// Standard GCE metadata flags
			"GCE_METADATA_HOST",
			"GCE_METADATA_IP",
			"GCE_METADATA_ROOT",

			// Siso
			"SISO_PROJECT",
			"SISO_CREDENTIAL_HELPER",
			"SISO_LIMITS",
		}, config.BuildBrokenNinjaUsesEnvVars()...)...)
	}

	cmd.Environment.Set("DIST_DIR", config.DistDir())
	cmd.Environment.Set("SHELL", "/bin/bash")
	switch config.ninjaCommand {
	case NINJA_N2:
		cmd.Environment.Set("RUST_BACKTRACE", "1")
	default:
		// Only set RUST_BACKTRACE for n2.
	}

	// Set up the metrics aggregation directory.
	ctx.ExecutionMetrics.SetDir(filepath.Join(config.OutDir(), "soong", "metrics_aggregation"))
	cmd.Environment.Set("SOONG_METRICS_AGGREGATION_DIR", ctx.ExecutionMetrics.MetricsAggregationDir)

	// Print the environment variables that Ninja is operating in.
	ctx.Verboseln("Ninja environment: ")
	envVars := cmd.Environment.Environ()
	sort.Strings(envVars)
	for _, envVar := range envVars {
		ctx.Verbosef("  %s", envVar)
	}

	// Write the env vars available during ninja execution to a file
	ninjaEnvVars := cmd.Environment.AsMap()
	data, err := shared.EnvFileContents(ninjaEnvVars)
	if err != nil {
		ctx.Panicf("Could not parse environment variables for ninja run %s", err)
	}
	// Write the file in every single run. This is fine because
	// 1. It is not a dep of Soong analysis, so will not retrigger Soong analysis.
	// 2. Is is fairly lightweight (~1Kb)
	ninjaEnvVarsFile := shared.JoinPath(config.SoongOutDir(), ninjaEnvFileName)
	err = os.WriteFile(ninjaEnvVarsFile, data, 0666)
	if err != nil {
		ctx.Panicf("Could not write ninja environment file %s", err)
	}

	// Poll the Ninja log for updates regularly based on the heartbeat
	// frequency. If it isn't updated enough, then we want to surface the
	// possibility that Ninja is stuck, to the user.
	done := make(chan struct{})
	defer close(done)
	ticker := time.NewTicker(ninjaHeartbeatDuration)
	defer ticker.Stop()
	ninjaChecker := &ninjaStucknessChecker{
		logPath: filepath.Join(config.OutDir(), ninjaLogFileName),
	}
	go func() {
		for {
			select {
			case <-ticker.C:
				ninjaChecker.check(ctx, config)
			case <-done:
				return
			}
		}
	}()

	ctx.ExecutionMetrics.Start()
	defer ctx.ExecutionMetrics.Finish(ExecutionMetricsFinishAdaptor{ctx})
	ctx.Status.Status("Starting ninja...")
	cmd.RunAndStreamOrFatal()

	// Post build execution.
	if config.ninjaCommand == NINJA_SISO {
		distFile(ctx, config, config.SisoConfigFile(false), "soong_ui/siso")
		distFile(ctx, config, config.SisoDepsFile(false), "soong_ui/siso")
		distFile(ctx, config, config.SisoFsStateFile(false), "soong_ui/siso")
		distFile(ctx, config, config.SisoFilegroupsFile(false), "soong_ui/siso")
	}
}

// A simple struct for checking if Ninja gets stuck, using timestamps.
type ninjaStucknessChecker struct {
	logPath     string
	prevModTime time.Time
}

// Check that a file has been modified since the last time it was checked. If
// the mod time hasn't changed, then assume that Ninja got stuck, and print
// diagnostics for debugging.
func (c *ninjaStucknessChecker) check(ctx Context, config Config) {
	info, err := os.Stat(c.logPath)
	var newModTime time.Time
	if err == nil {
		newModTime = info.ModTime()
	}
	if newModTime == c.prevModTime {
		// The Ninja file hasn't been modified since the last time it was
		// checked, so Ninja could be stuck. Output some diagnostics.
		ctx.Verbosef("ninja may be stuck; last update to %v was %v. dumping process tree...", c.logPath, newModTime)
		ctx.Printf("ninja may be stuck, check %v for list of running processes.",
			filepath.Join(config.LogsDir(), config.logsPrefix+"soong.log"))

		// The "pstree" command doesn't exist on Mac, but "pstree" on Linux
		// gives more convenient output than "ps" So, we try pstree first, and
		// ps second
		commandText := fmt.Sprintf("pstree -palT %v || ps -ef", os.Getpid())

		cmd := Command(ctx, config, nil, "dump process tree", "bash", "-c", commandText)
		output := cmd.CombinedOutputOrFatal()
		ctx.Verbose(string(output))

		ctx.Verbosef("done\n")
	}
	c.prevModTime = newModTime
}
