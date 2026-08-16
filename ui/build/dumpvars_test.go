// Copyright 2026 The uwuAOSP Project
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     https://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package build

import (
	"strings"
	"testing"
)

func TestRenderBannerPlainText(t *testing.T) {
	got := renderBanner(map[string]string{
		"TARGET_PRODUCT":            "uwu_fajita",
		"TARGET_RELEASE":            "cp2a",
		"TARGET_BUILD_VARIANT":      "userdebug",
		"TARGET_DEVICE":             "fajita",
		"TARGET_ARCH":               "arm64",
		"TARGET_ARCH_VARIANT":       "armv8-2a",
		"TARGET_2ND_ARCH":           "arm",
		"TARGET_2ND_ARCH_VARIANT":   "armv8-a",
		"UWU_VERSION":               "17.0",
		"PLATFORM_VERSION":          "17",
		"PLATFORM_VERSION_CODENAME": "REL",
		"BUILD_ID":                  "BP2A.250605.031.A3",
		"OUT_DIR":                   "out",
		"HOST_OS_EXTRA":             "Linux-6.17.0-x86_64",
	}, bannerState{
		incremental: true,
	}, false, false)

	want := []string{
		"  uwuAOSP",
		"Target\n------",
		"Product           uwu_fajita",
		"Architecture      arm64 (armv8-2a) / arm (armv8-a)",
		"Platform\n--------",
		"uwuAOSP           17.0",
		"Build\n-----",
		"Incremental       true",
		"Host\n----",
		"OS                Linux-6.17.0-x86_64",
		"==> Build environment ready",
	}
	for _, value := range want {
		if !strings.Contains(got, value) {
			t.Errorf("banner does not contain %q:\n%s", value, got)
		}
	}
	if strings.Contains(got, "\033[") {
		t.Fatalf("plain text banner contains ANSI escapes:\n%q", got)
	}
	if strings.Contains(got, "'") {
		t.Fatalf("banner contains a single quote and cannot be safely cached by dumpvars: %q", got)
	}
}

func TestRenderBannerTrueColor(t *testing.T) {
	got := renderBanner(map[string]string{
		"TARGET_PRODUCT": "uwu_fajita",
	}, bannerState{}, true, true)

	if !strings.Contains(got, "\033[38;2;141;227;253m") {
		t.Fatalf("true color banner does not contain the uwuAOSP brand gradient: %q", got)
	}
	plain := got
	for _, char := range "uwuAOSP" {
		if !strings.Contains(plain, string(char)) {
			t.Fatalf("true color banner is missing brand character %q: %q", char, got)
		}
	}
}
