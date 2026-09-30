// Copyright 2020 Google Inc. All rights reserved.
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

package android

import (
	"io"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/google/blueprint"
)

//go:generate go run ../../blueprint/gobtools/codegen

type phonyMap map[string]Paths

// @auto-generate: gob
type PhonyInfo struct {
	Phonies phonyMap
}

var SingletonPhonyProvider = blueprint.NewSingletonProvider[PhonyInfo]()

type phonySingleton struct {
	phonyMap  phonyMap
	phonyList []string

	soongDist
}

const soongPhonyNinjaShardCount = 1024

const soongPhonyNinjaShardWriteConcurrency = 8

func soongPhonyNinjaShardIndex(target string) int {
	const offset32 = 2166136261
	const prime32 = 16777619
	hash := uint32(offset32)
	for i := 0; i < len(target); i++ {
		hash ^= uint32(target[i])
		hash *= prime32
	}
	return int(hash % soongPhonyNinjaShardCount)
}

func shardSoongPhonyTargets(targets []string) [][]string {
	shards := make([][]string, soongPhonyNinjaShardCount)
	for _, target := range targets {
		shard := soongPhonyNinjaShardIndex(target)
		shards[shard] = append(shards[shard], target)
	}
	return shards
}

var _ SingletonMakeVarsProvider = (*phonySingleton)(nil)

func (p *phonySingleton) GenerateBuildActions(ctx SingletonContext) {
	p.phonyMap = make(phonyMap)
	ctx.VisitAllModuleProxies(func(m ModuleProxy) {
		if info, ok := OtherModuleProvider(ctx, m, CommonModuleInfoProvider); ok && info.Phonies != nil {
			for k, v := range info.Phonies.Phonies {
				p.phonyMap[k] = append(p.phonyMap[k], v...)
			}
		}
	})

	ctx.VisitAllSingletons(func(s blueprint.SingletonProxy) {
		if info, ok := OtherSingletonProvider(ctx, s, SingletonPhonyProvider); ok {
			for k, v := range info.Phonies {
				p.phonyMap[k] = append(p.phonyMap[k], v...)
			}
		}
	})

	if !ctx.Config().KatiEnabled() {
		p.soongDist.collectDists(ctx)
		// Every dist goal gets an extra dependency on an extra phony with a __dist suffix.  On builds without dist
		// enabled the phony will have no dependencies, on builds with dist enabled the phony will depend on all
		// the copy rules for that goal.
		p.soongDist.addDistsToPhonyMap(ctx, p.phonyMap)

		// Define well-known goals and their dependency graph that they've
		// traditionally had in make builds.
		addPhony := func(target string, deps ...Path) {
			p.phonyMap[target] = append(p.phonyMap[target], deps...)
		}
		addPhony("droid", PathForPhony(ctx, "droid_targets"))
		addPhony("droid_targets", PathForPhony(ctx, "droidcore"), PathForPhony(ctx, "dist_files"))
		addPhony("droidcore", PathForPhony(ctx, "droidcore-unbundled"))
		addPhony("dist_files")
		addPhony("droidcore-unbundled")
	}

	// We will sort phonyList in parallel with other stuff later, but for now copy it into
	// a slice in series so that we don't read and write to phonyMap concurrently.
	p.phonyList = make([]string, 0, len(p.phonyMap))
	for phony := range p.phonyMap {
		p.phonyList = append(p.phonyList, phony)
	}

	type phonyDef struct {
		name string
		deps Paths
	}

	sortChan := make(chan phonyDef, len(p.phonyMap))
	resultsChan := make(chan phonyDef)
	var wg sync.WaitGroup

	// Sorting the phony deps in parallel saves about 2 seconds. Nothing runs in parallel with
	// the phony singleton so it's time off of wall clock.
	for i := 0; i < 2*runtime.NumCPU(); i++ {
		wg.Add(1)
		go func() {
			for toSort := range sortChan {
				toSort.deps = SortedUniquePaths(toSort.deps)
				resultsChan <- toSort
			}
			wg.Done()
		}()
	}

	go func() {
		sort.Strings(p.phonyList)
		wg.Wait()
		close(resultsChan)
	}()

	for phony, deps := range p.phonyMap {
		sortChan <- phonyDef{
			name: phony,
			deps: deps,
		}
	}
	close(sortChan)

	for result := range resultsChan {
		p.phonyMap[result.name] = result.deps
	}

	if !ctx.Config().KatiEnabled() {
		// The filename suffix style used by soong (".$TARGET_PRODUCT") is not available here, fake it
		// using katiSuffix ("-$TARGET_PRODUCT").
		suffix := "." + strings.TrimPrefix(ctx.Config().katiSuffix, "-")
		soongPhonyNinja := PathForOutput(ctx, "build"+suffix+".phony.ninja")
		soongDistFile := PathForOutput(ctx, "build"+suffix+".dist.ninja")
		soongNoDistFile := PathForOutput(ctx, "build"+suffix+".nodist.ninja")
		ctx.addSubninja(soongPhonyNinja.String())
		ctx.addSubninja(PathForOutput(ctx).String() + "/build" + suffix + ".$dist.ninja")
		phonyShards := shardSoongPhonyTargets(p.phonyList)
		phonyShardPaths := make([]string, len(phonyShards))
		for i := range phonyShards {
			phonyShardPaths[i] = PathForOutput(ctx, "build"+suffix+".phony."+strconv.Itoa(i)+".ninja").String()
		}

		wg := WaitGroupWithErrorCollector{}

		wg.Go(func() error {
			f, err := openBufferedFileWithBufferSize(absolutePath(soongPhonyNinja.String()), 64*1024)
			if err != nil {
				return err
			}
			defer f.Close()
			return p.writeSoongPhonyNinja(f, phonyShardPaths)
		})

		shardWriteSlots := make(chan struct{}, soongPhonyNinjaShardWriteConcurrency)
		for i, targets := range phonyShards {
			shardPath := phonyShardPaths[i]
			wg.Go(func() error {
				shardWriteSlots <- struct{}{}
				defer func() { <-shardWriteSlots }()
				f, err := openBufferedFileWithBufferSize(absolutePath(shardPath), 64*1024)
				if err != nil {
					return err
				}
				defer f.Close()
				return p.writeSoongPhonyNinjaShard(f, targets)
			})
		}

		wg.GoWithMultipleErrors(func() []error {
			return p.soongDist.writeNinjaFiles(soongDistFile, soongNoDistFile, ctx.Config().REWrapperRemoteBuild())
		})

		wg.Wait()

		for _, err := range wg.Errors() {
			ctx.Errorf("%s", err)
		}
	}
}

func (p phonySingleton) MakeVars(ctx MakeVarsContext) {
	for _, phony := range p.phonyList {
		ctx.Phony(phony, p.phonyMap[phony]...)
	}
}

func phonySingletonFactory() Singleton {
	return &phonySingleton{}
}

func (p *phonySingleton) IncrementalSupported() bool {
	return true
}

// writeSoongPhonyNinja writes a ninja file containing phony rules for each phony target requested in Soong-only builds.
func (p *phonySingleton) writeSoongPhonyNinja(w io.StringWriter, shardPaths []string) error {
	var err error
	write := func(s string) {
		if err == nil {
			_, err = w.WriteString(s)
		}
	}
	for _, shardPath := range shardPaths {
		write("subninja ")
		write(shardPath)
		write("\n")
	}
	write("default droid\n")
	return err
}

func (p *phonySingleton) writeSoongPhonyNinjaShard(w io.StringWriter, targets []string) error {
	var err error
	write := func(s string) {
		if err == nil {
			_, err = w.WriteString(s)
		}
	}
	for _, phony := range targets {
		write("build ")
		write(phony)
		write(": phony")
		for _, dep := range p.phonyMap[phony] {
			write(" ")
			write(dep.String())
		}
		write("\n")
		write(" phony_output = true\n")
	}
	return err
}
