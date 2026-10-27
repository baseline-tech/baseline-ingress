// Copyright 2026 LiveKit, Inc.
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

package media

import (
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/logger"
)

// ProbeGPU smoke encodes a few frames through each GPU tier, records the best working tier in gpu
// so handlers inherit it, and turns gpu.enabled off when none works so sessions use the CPU.
func ProbeGPU(gpu *config.GPUConfig) {
	if !gpu.Enabled {
		return
	}

	gst.Init(nil)

	first := gpu.Tier
	if !first.UsesGPU() {
		first = config.GPUTierNvidia
	}
	tier, failures := resolveVideoTier(*gpu, first, 1)
	for _, f := range failures {
		logger.Warnw("gpu tier failed its smoke encode", f.err, "tier", f.tier)
	}
	if !tier.UsesGPU() {
		logger.Warnw("gpu transcoding unavailable, using the cpu", nil)
		gpu.Enabled = false
		return
	}

	gpu.Tier = tier
	logger.Infow("gpu transcoding enabled", "tier", tier, "maxEncoderSessions", gpu.MaxEncoderSessions, "cpuCost", gpu.CPUCost, "preset", gpu.Preset)
}
