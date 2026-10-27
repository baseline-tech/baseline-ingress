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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/config"
)

func TestProbeGPUDisabledIsANoop(t *testing.T) {
	gpu := config.GPUConfig{}
	ProbeGPU(&gpu)
	require.False(t, gpu.Enabled)
	require.Empty(t, gpu.Tier, "a disabled gpu writes no tier into the handler config")
}

func TestProbeGPUFallsBackWithoutNvcodec(t *testing.T) {
	requireNoNvidiaEncoder(t)

	gpu := config.GPUConfig{Enabled: true, MaxEncoderSessions: 1, CPUCost: 1, Preset: "p3", Tier: config.GPUTierNvidia}
	ProbeGPU(&gpu)
	require.False(t, gpu.Enabled)
}

func TestProbeGPURecordsTheTier(t *testing.T) {
	requireNvidiaEncoder(t)

	gpu := config.GPUConfig{Enabled: true, MaxEncoderSessions: 1, CPUCost: 1, Preset: "p3", Tier: config.GPUTierNvidia}
	ProbeGPU(&gpu)
	require.True(t, gpu.Enabled)
	require.True(t, gpu.Tier.UsesGPU(), "the probe records a gpu tier for handlers")
}
