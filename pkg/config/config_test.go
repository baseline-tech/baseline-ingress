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

	conf.GPU.setDefaults()
	require.Equal(t, DefaultGPUMaxSessions, conf.GPU.MaxSessions)
	require.Equal(t, DefaultGPUCPUCost, conf.GPU.CPUCost)

	conf.GPU.MaxSessions, conf.GPU.CPUCost = 3, 1.5
	conf.GPU.setDefaults()
	require.Equal(t, 3, conf.GPU.MaxSessions)
	require.Equal(t, 1.5, conf.GPU.CPUCost)
}

func TestGPUConfigEncodes(t *testing.T) {
	require.True(t, GPUConfig{Enabled: true}.Encodes(livekit.VideoCodec_H264_BASELINE))
	require.False(t, GPUConfig{Enabled: true}.Encodes(livekit.VideoCodec_VP8))
	require.False(t, GPUConfig{}.Encodes(livekit.VideoCodec_H264_BASELINE))
}
