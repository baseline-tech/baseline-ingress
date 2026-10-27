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

package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/protocol/livekit"
)

func TestGPUConfigDefaults(t *testing.T) {
	conf, err := NewConfig("redis:\n  address: localhost:6379\ngpu:\n  enabled: true\n")
	require.NoError(t, err)
	require.True(t, conf.GPU.Enabled)

	require.NoError(t, conf.GPU.initDefaults())
	require.Equal(t, DefaultGPUMaxEncoderSessions, conf.GPU.MaxEncoderSessions)
	require.Equal(t, DefaultGPUCPUCost, conf.GPU.CPUCost)
	require.Equal(t, DefaultGPUPreset, conf.GPU.Preset)
	require.Equal(t, GPUTierNvidia, conf.GPU.Tier, "enabled means the full tier until the probe says otherwise")

	conf.GPU.MaxEncoderSessions, conf.GPU.CPUCost, conf.GPU.Preset, conf.GPU.Tier = 3, 1.5, "p7", GPUTierCPU
	require.NoError(t, conf.GPU.initDefaults())
	require.Equal(t, 3, conf.GPU.MaxEncoderSessions)
	require.Equal(t, 1.5, conf.GPU.CPUCost)
	require.Equal(t, "p7", conf.GPU.Preset)
	require.Equal(t, GPUTierCPU, conf.GPU.Tier)
}

func TestGPUConfigRejectsUnknownPreset(t *testing.T) {
	conf, err := NewConfig("redis:\n  address: localhost:6379\ngpu:\n  enabled: true\n  preset: hq\n")
	require.NoError(t, err)
	require.ErrorContains(t, conf.InitDefaults(), `gpu preset "hq"`)

	conf, err = NewConfig("redis:\n  address: localhost:6379\ngpu:\n  preset: hq\n")
	require.NoError(t, err)
	require.NoError(t, conf.InitDefaults(), "the preset is not validated while the gpu is off")
}

func TestGPUConfigEncodes(t *testing.T) {
	require.True(t, GPUConfig{Enabled: true, Tier: GPUTierNvidia}.Encodes(livekit.VideoCodec_H264_BASELINE))
	require.True(t, GPUConfig{Enabled: true, Tier: GPUTierNvidiaEncode}.Encodes(livekit.VideoCodec_H264_BASELINE))
	require.False(t, GPUConfig{Enabled: true, Tier: GPUTierNvidia}.Encodes(livekit.VideoCodec_VP8))
	require.False(t, GPUConfig{Enabled: true, Tier: GPUTierCPU}.Encodes(livekit.VideoCodec_H264_BASELINE), "a session spilled over to the cpu")
	require.False(t, GPUConfig{Enabled: true}.Encodes(livekit.VideoCodec_H264_BASELINE), "no tier until the probe or defaults set one")
	require.False(t, GPUConfig{Tier: GPUTierNvidia}.Encodes(livekit.VideoCodec_H264_BASELINE))
}

func TestGPUTierOrder(t *testing.T) {
	require.Equal(t, GPUTierNvidiaEncode, GPUTierNvidia.Next())
	require.Equal(t, GPUTierCPU, GPUTierNvidiaEncode.Next())
	require.Equal(t, GPUTierCPU, GPUTierCPU.Next())
	require.True(t, GPUTierNvidia.UsesGPU())
	require.True(t, GPUTierNvidiaEncode.UsesGPU())
	require.False(t, GPUTierCPU.UsesGPU())
}
