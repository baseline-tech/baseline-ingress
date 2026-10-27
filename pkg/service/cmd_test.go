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

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/params"
)

func handlerGPU(t *testing.T, p *params.Params) config.GPUConfig {
	t.Helper()
	body, err := handlerConfigBody(p)
	require.NoError(t, err)
	conf, err := config.NewConfig(string(body))
	require.NoError(t, err)
	return conf.GPU
}

func TestHandlerConfigCarriesTheGPUDecision(t *testing.T) {
	conf, err := config.NewConfig("redis:\n  address: localhost:6379\ngpu:\n  enabled: true\n  preset: p5\n")
	require.NoError(t, err)
	require.NoError(t, conf.InitDefaults())
	conf.GPU.Tier = config.GPUTierNvidiaEncode // what the probe resolved

	admitted := &params.Params{Config: conf, GPUEncoders: 3}
	require.Equal(t, conf.GPU, handlerGPU(t, admitted), "a gpu admitted session inherits the probed tier")

	spilled := &params.Params{Config: conf, GPUEncoders: 0}
	gpu := handlerGPU(t, spilled)
	require.Equal(t, config.GPUTierCPU, gpu.Tier, "a spilled session transcodes on the cpu")
	require.True(t, gpu.Enabled)
	require.Equal(t, "p5", gpu.Preset)
	require.Equal(t, config.GPUTierNvidiaEncode, conf.GPU.Tier, "the service config is not touched")
}

func TestHandlerConfigWithGPUDisabledIsUpstream(t *testing.T) {
	conf, err := config.NewConfig("redis:\n  address: localhost:6379\n")
	require.NoError(t, err)
	require.NoError(t, conf.InitDefaults())

	body, err := handlerConfigBody(&params.Params{Config: conf})
	require.NoError(t, err)
	upstream, err := yaml.Marshal(conf)
	require.NoError(t, err)
	require.Equal(t, string(upstream), string(body), "the handler config is byte for byte the upstream one while the gpu is off")
	require.NotContains(t, string(body), "tier")
}
