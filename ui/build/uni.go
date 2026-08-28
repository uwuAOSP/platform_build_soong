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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const uniStateVersion = 7

type uniGraphFile struct {
	Path        string `json:"path"`
	Size        int64  `json:"size"`
	ModTimeNano int64  `json:"mod_time_nano"`
}

type uniState struct {
	Version           int            `json:"version"`
	SourceRoot        string         `json:"source_root"`
	OutDir            string         `json:"out_dir"`
	ProductOut        string         `json:"product_out"`
	TargetProduct     string         `json:"target_product"`
	TargetDevice      string         `json:"target_device"`
	TargetRelease     string         `json:"target_release"`
	BuildVariant      string         `json:"build_variant"`
	BuildDateTime     string         `json:"build_date_time"`
	BuildDateTimeFile string         `json:"build_date_time_file"`
	KatiSuffix        string         `json:"kati_suffix"`
	SkipKatiNinja     bool           `json:"skip_kati_ninja"`
	CombinedNinja     string         `json:"combined_ninja"`
	SoongNinja        string         `json:"soong_ninja"`
	SoongVariables    string         `json:"soong_variables"`
	KatiEnvironment   string         `json:"kati_environment"`
	KatiBuildNinja    string         `json:"kati_build_ninja"`
	KatiPackageNinja  string         `json:"kati_package_ninja"`
	OriginalArgs      []string       `json:"original_args"`
	NinjaArgs         []string       `json:"ninja_args"`
	ProductPackages   []string       `json:"product_packages"`
	AllModules        []string       `json:"all_modules"`
	R8Modules         []string       `json:"r8_modules"`
	R8ModulesReady    bool           `json:"r8_modules_ready"`
	TaskMetadata      string         `json:"task_metadata,omitempty"`
	Dist              bool           `json:"dist"`
	GraphFingerprint  string         `json:"graph_fingerprint"`
	GraphFiles        []uniGraphFile `json:"graph_files"`
}

type uniProductVariables struct {
	PartitionVars struct {
		ProductPackagesSet map[string]struct {
			ProductPackages []string
		}
	} `json:"PartitionVarsForSoongMigrationOnlyDoNotUse"`
}

func absolutePath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(path)
}

func readUniProductPackages(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var variables uniProductVariables
	if err := json.Unmarshal(data, &variables); err != nil {
		return nil, err
	}
	packages := variables.PartitionVars.ProductPackagesSet["all"].ProductPackages
	sort.Strings(packages)
	return packages, nil
}

func readUniLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	lines := strings.Fields(string(data))
	sort.Strings(lines)
	return lines, nil
}

func statUniGraphFiles(paths ...string) ([]uniGraphFile, string, error) {
	files := make([]uniGraphFile, 0, len(paths))
	hash := sha256.New()
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, "", err
		}
		file := uniGraphFile{Path: path, Size: info.Size(), ModTimeNano: info.ModTime().UnixNano()}
		files = append(files, file)
		fmt.Fprintf(hash, "%s\x00%d\x00%d\n", file.Path, file.Size, file.ModTimeNano)
	}
	return files, hex.EncodeToString(hash.Sum(nil)), nil
}

func collectUniGraphFiles(soongNinja string, paths ...string) ([]uniGraphFile, string, error) {
	pattern := strings.TrimSuffix(soongNinja, filepath.Ext(soongNinja)) + ".*.ninja"
	shards, err := filepath.Glob(pattern)
	if err != nil {
		return nil, "", err
	}
	sort.Strings(shards)
	graphPaths := make([]string, 0, 1+len(shards)+len(paths))
	graphPaths = append(graphPaths, soongNinja)
	graphPaths = append(graphPaths, shards...)
	graphPaths = append(graphPaths, paths...)
	return statUniGraphFiles(graphPaths...)
}

func fingerprintUniGraphFilesWithMutableDate(current, expected []uniGraphFile, mutablePath string) (string, error) {
	if len(current) != len(expected) {
		return "", nil
	}
	hash := sha256.New()
	for index, file := range current {
		if filepath.Clean(file.Path) != filepath.Clean(expected[index].Path) {
			return "", nil
		}
		info, err := os.Stat(file.Path)
		if err != nil {
			return "", err
		}
		size := info.Size()
		modTime := info.ModTime().UnixNano()
		if filepath.Clean(file.Path) == filepath.Clean(mutablePath) {
			size = expected[index].Size
			modTime = expected[index].ModTimeNano
		}
		fmt.Fprintf(hash, "%s\x00%d\x00%d\n", file.Path, size, modTime)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

// WriteUniState records the graph identity needed by the standalone scheduler.
// It is called after Soong and Kati finish and before any Ninja action runs.
func WriteUniState(config Config, originalArgs []string) error {
	statePath, ok := config.Environment().Get("UNI_STATE_FILE")
	if !ok || statePath == "" {
		return fmt.Errorf("UNI_STATE_FILE is not set")
	}

	sourceRoot := os.Getenv("TOP")
	if sourceRoot == "" {
		sourceRoot = "."
	}
	var err error
	if sourceRoot, err = absolutePath(sourceRoot); err != nil {
		return err
	}
	outDir, err := absolutePath(config.OutDir())
	if err != nil {
		return err
	}
	productOut, err := absolutePath(config.ProductOut())
	if err != nil {
		return err
	}
	combinedNinja, err := absolutePath(config.CombinedNinjaFile())
	if err != nil {
		return err
	}
	soongNinja, err := absolutePath(config.SoongNinjaFile())
	if err != nil {
		return err
	}
	soongVariables, err := absolutePath(config.SoongVarsFile())
	if err != nil {
		return err
	}
	katiEnvironment, err := absolutePath(config.KatiEnvFile())
	if err != nil {
		return err
	}
	katiBuildNinja, err := absolutePath(config.KatiBuildNinjaFile())
	if err != nil {
		return err
	}
	katiPackageNinja, err := absolutePath(config.KatiPackageNinjaFile())
	if err != nil {
		return err
	}
	buildDateTimeFile, ok := config.Environment().Get("BUILD_DATETIME_FILE")
	if !ok || buildDateTimeFile == "" {
		return fmt.Errorf("BUILD_DATETIME_FILE is not set")
	}
	buildDateTimeFile, err = absolutePath(buildDateTimeFile)
	if err != nil {
		return err
	}

	productPackages, err := readUniProductPackages(soongVariables)
	if err != nil {
		return fmt.Errorf("read product packages: %w", err)
	}
	allModulesPath := filepath.Join(productOut, "all_modules.txt")
	allModules, err := readUniLines(allModulesPath)
	if err != nil {
		return fmt.Errorf("read all modules: %w", err)
	}
	var r8Modules []string
	r8ModulesReady := false
	if r8ModulesPath, ok := config.Environment().Get("UNI_R8_MODULES_FILE"); ok && r8ModulesPath != "" {
		if r8Modules, err = readUniLines(r8ModulesPath); err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("read R8 modules: %w", err)
			}
		} else {
			r8ModulesReady = true
		}
	}
	taskMetadata := ""
	if taskMetadataPath, ok := config.Environment().Get("UNI_TASK_METADATA_FILE"); ok && taskMetadataPath != "" {
		taskMetadata, err = absolutePath(taskMetadataPath)
		if err != nil {
			return err
		}
	}
	graphPaths := []string{
		combinedNinja, soongVariables, katiEnvironment, katiBuildNinja, katiPackageNinja,
		allModulesPath, buildDateTimeFile,
	}
	if taskMetadata != "" {
		graphPaths = append(graphPaths, taskMetadata)
	}
	graphFiles, fingerprint, err := collectUniGraphFiles(soongNinja, graphPaths...)
	if err != nil {
		return fmt.Errorf("stat build graph: %w", err)
	}
	targetRelease, _ := config.Environment().Get("TARGET_RELEASE")

	state := uniState{
		Version:           uniStateVersion,
		SourceRoot:        sourceRoot,
		OutDir:            outDir,
		ProductOut:        productOut,
		TargetProduct:     config.TargetProduct(),
		TargetDevice:      config.TargetDevice(),
		TargetRelease:     targetRelease,
		BuildVariant:      config.TargetBuildVariant(),
		BuildDateTime:     config.BuildDateTime(),
		BuildDateTimeFile: buildDateTimeFile,
		KatiSuffix:        config.KatiSuffix(),
		SkipKatiNinja:     config.SkipKatiNinja(),
		CombinedNinja:     combinedNinja,
		SoongNinja:        soongNinja,
		SoongVariables:    soongVariables,
		KatiEnvironment:   katiEnvironment,
		KatiBuildNinja:    katiBuildNinja,
		KatiPackageNinja:  katiPackageNinja,
		OriginalArgs:      append([]string(nil), originalArgs...),
		NinjaArgs:         append([]string(nil), config.NinjaArgs()...),
		ProductPackages:   productPackages,
		AllModules:        allModules,
		R8Modules:         r8Modules,
		R8ModulesReady:    r8ModulesReady,
		TaskMetadata:      taskMetadata,
		Dist:              config.Dist(),
		GraphFingerprint:  fingerprint,
		GraphFiles:        graphFiles,
	}

	statePath, err = absolutePath(statePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0777); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(statePath), ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0666); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, statePath)
}

// LoadUniState restores the product identity that product config normally
// supplies. Ninja-only phases intentionally skip that product config work.
func LoadUniState(config Config) error {
	statePath, ok := config.Environment().Get("UNI_STATE_FILE")
	if !ok || statePath == "" {
		return fmt.Errorf("UNI_STATE_FILE is not set")
	}
	data, err := os.ReadFile(statePath)
	if err != nil {
		return err
	}
	var state uniState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	if state.Version != uniStateVersion {
		return fmt.Errorf("unsupported uni state version %d", state.Version)
	}
	if state.BuildDateTime == "" || state.BuildDateTimeFile == "" {
		return fmt.Errorf("uni state is missing build date information")
	}
	if state.TargetProduct != config.TargetProduct() {
		return fmt.Errorf("uni state product %q does not match %q", state.TargetProduct, config.TargetProduct())
	}
	outDir, err := absolutePath(config.OutDir())
	if err != nil {
		return err
	}
	if filepath.Clean(state.OutDir) != filepath.Clean(outDir) {
		return fmt.Errorf("uni output directory changed")
	}
	graphPaths := []string{
		state.CombinedNinja, state.SoongVariables, state.KatiEnvironment,
		state.KatiBuildNinja, state.KatiPackageNinja,
		filepath.Join(state.ProductOut, "all_modules.txt"), state.BuildDateTimeFile,
	}
	if state.TaskMetadata != "" {
		graphPaths = append(graphPaths, state.TaskMetadata)
	}
	currentFiles, fingerprint, err := collectUniGraphFiles(state.SoongNinja, graphPaths...)
	if err != nil {
		return err
	}
	if fingerprint != state.GraphFingerprint {
		fingerprint, err = fingerprintUniGraphFilesWithMutableDate(currentFiles, state.GraphFiles, state.BuildDateTimeFile)
		if err != nil {
			return err
		}
		if fingerprint != state.GraphFingerprint {
			return fmt.Errorf("uni build graph changed")
		}
	}
	config.SetTargetDevice(state.TargetDevice)
	config.SetKatiSuffix(state.KatiSuffix)
	config.SetSkipKatiNinja(state.SkipKatiNinja)
	config.buildDateTime = state.BuildDateTime
	config.Environment().Set("BUILD_DATETIME", state.BuildDateTime)
	config.Environment().Set("BUILD_DATETIME_FILE", state.BuildDateTimeFile)
	if combined, err := absolutePath(config.CombinedNinjaFile()); err != nil {
		return err
	} else if filepath.Clean(combined) != filepath.Clean(state.CombinedNinja) {
		return fmt.Errorf("uni combined Ninja path changed")
	}
	config.SetUniCombinedNinjaFile(filepath.Join(state.OutDir, "uni", state.TargetProduct, "combined.ninja"))
	return nil
}
