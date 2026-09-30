/*
 * SPDX-FileCopyrightText: The uwuAOSP Project
 * SPDX-License-Identifier: Apache-2.0
 */

package android

import (
	"encoding/json"
	"fmt"

	"github.com/google/blueprint"
)

// ModuleForcedDisabledStateCache snapshots the module state changed by
// mutators that only call ModuleBase.Disable. It intentionally avoids
// restoring registered properties, which later mutators may initialize or
// transform independently.
type ModuleForcedDisabledStateCache struct {
	ModuleFilter func(Module) bool
}

var _ blueprint.MutatorStateCacheModuleFilter = ModuleForcedDisabledStateCache{}
var _ blueprint.MutatorStateCacheVersion = ModuleForcedDisabledStateCache{}

func (ModuleForcedDisabledStateCache) CacheVersion() string {
	return "forced-disabled-v1"
}

func (cache ModuleForcedDisabledStateCache) ShouldRun(module blueprint.Module) bool {
	androidModule, ok := module.(Module)
	if !ok {
		return false
	}
	return cache.ModuleFilter == nil || cache.ModuleFilter(androidModule)
}

func (cache ModuleForcedDisabledStateCache) Snapshot(module blueprint.Module) ([]byte, error) {
	if !cache.ShouldRun(module) {
		return nil, nil
	}
	androidModule, ok := module.(Module)
	if !ok {
		return nil, fmt.Errorf("module forced-disabled cache selected non-Android module %T", module)
	}
	return json.Marshal(androidModule.base().commonProperties.ForcedDisabled)
}

func (cache ModuleForcedDisabledStateCache) Restore(module blueprint.Module, state []byte) error {
	if !cache.ShouldRun(module) {
		return nil
	}
	if len(state) == 0 {
		return nil
	}
	androidModule, ok := module.(Module)
	if !ok {
		return fmt.Errorf("cached forced-disabled state belongs to non-Android module %T", module)
	}
	return json.Unmarshal(state, &androidModule.base().commonProperties.ForcedDisabled)
}
