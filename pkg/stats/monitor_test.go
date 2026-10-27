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

package stats

import (
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/protocol/livekit"
	"github.com/livekit/psrpc"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/errors"
)

func TestPublicationStatus(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error is success",
			err:      nil,
			expected: publicationStatusSuccess,
		},
		{
			name:     "unstructured error is internal",
			err:      errors.New("boom"),
			expected: publicationStatusInternal,
		},
		{
			name:     "HTTPError 4xx is 4xx",
			err:      errors.NewHTTPError(http.StatusBadRequest, errors.New("bad request")),
			expected: publicationStatus4xx,
		},
		{
			name:     "HTTPError 5xx is 5xx",
			err:      errors.NewHTTPError(http.StatusBadGateway, errors.New("upstream down")),
			expected: publicationStatus5xx,
		},
		{
			name:     "HTTPError non-error status is internal",
			err:      errors.NewHTTPError(http.StatusOK, errors.New("weird")),
			expected: publicationStatusInternal,
		},
		{
			name:     "HTTPError 5xx wrapped in psrpc is still 5xx",
			err:      psrpc.NewError(psrpc.Internal, errors.NewHTTPError(http.StatusServiceUnavailable, errors.New("unavailable"))),
			expected: publicationStatus5xx,
		},
		{
			name:     "psrpc 4xx code is 4xx",
			err:      psrpc.NewErrorf(psrpc.InvalidArgument, "bad input"),
			expected: publicationStatus4xx,
		},
		{
			name:     "psrpc internal code is internal",
			err:      psrpc.NewErrorf(psrpc.Internal, "boom"),
			expected: publicationStatusInternal,
		},
		{
			name:     "psrpc 5xx code is internal, not 5xx",
			err:      psrpc.NewErrorf(psrpc.Unavailable, "unavailable"),
			expected: publicationStatusInternal,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.expected, publicationStatus(c.err))
		})
	}
}

const (
	burnCPUEnv = "INGRESS_TEST_BURN_CPU"
	// burnCPU spans at least one tick of the 1s per-process sampler
	burnCPU = 2 * time.Second
)

// TestHelperBurnCPU is the handler stand-in run by TestSessionUsage: it uses burnCPU of CPU, then exits.
func TestHelperBurnCPU(t *testing.T) {
	if os.Getenv(burnCPUEnv) == "" {
		t.Skip("runs only as the child of TestSessionUsage")
	}
	var ru syscall.Rusage
	for time.Duration(ru.Utime.Nano()+ru.Stime.Nano()) < burnCPU {
		_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	}
}

func TestSessionUsage(t *testing.T) {
	conf, err := config.NewConfig(`
redis:
  address: localhost:6379
ws_url: ws://localhost:7880
api_key: key
api_secret: secret
cpu_cost:
  url_cpu_cost: 0.0001
`)
	require.NoError(t, err)

	m := NewMonitor()
	require.NoError(t, m.Start(conf))
	t.Cleanup(m.Stop)

	info := &livekit.IngressInfo{
		IngressId:         "IN_test",
		InputType:         livekit.IngressInput_URL_INPUT,
		EnableTranscoding: proto.Bool(true),
		State:             &livekit.IngressState{ResourceId: "res_test"},
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperBurnCPU$")
	cmd.Env = append(os.Environ(), burnCPUEnv+"=1")
	require.NoError(t, cmd.Start())
	m.HandlerStarted(info, cmd.Process.Pid)
	require.NoError(t, cmd.Wait())
	m.HandlerExited(cmd.Process.Pid, cmd.ProcessState)

	u := m.SessionEnded("res_test")
	require.NotNil(t, u)
	require.Equal(t, "IN_test", u.IngressID)
	require.Equal(t, livekit.IngressInput_URL_INPUT, u.InputType)
	require.True(t, u.Transcoding)
	require.GreaterOrEqual(t, u.CPUTime, burnCPU, "the handler used burnCPU of CPU")
	require.Positive(t, u.Duration)
	require.Positive(t, u.AvgCPU())
	if runtime.GOOS == "linux" {
		// per-process sampling reads procfs
		require.Positive(t, u.MaxCPU)
		require.NotZero(t, u.MaxMemory)
	}

	require.Nil(t, m.SessionEnded("res_test"), "usage is reported once")
}

func gpuTestMonitor(t *testing.T, gpuYAML string) *Monitor {
	t.Helper()
	conf, err := config.NewConfig(`
redis:
  address: localhost:6379
ws_url: ws://localhost:7880
api_key: key
api_secret: secret
cpu_cost:
  min_idle_ratio: 0.01
  rtmp_cpu_cost: 0.0001
  whip_cpu_cost: 0.0001
  whip_bypass_transcoding_cpu_cost: 0.0001
  url_cpu_cost: 0.0001
gpu:
` + gpuYAML)
	require.NoError(t, err)
	require.NoError(t, conf.InitDefaults())
	conf.GPU.Tier = config.GPUTierNvidia

	m := NewMonitor()
	require.NoError(t, m.Start(conf))
	t.Cleanup(m.Stop)
	require.Eventually(t, m.CanAccept, 5*time.Second, 50*time.Millisecond, "the cpu sampler reports idle time")
	return m
}

// rtmpInfo is a transcoded RTMP session with the default three simulcast layers.
func rtmpInfo(resourceID string) *livekit.IngressInfo {
	return &livekit.IngressInfo{
		IngressId:         "in_" + resourceID,
		InputType:         livekit.IngressInput_RTMP_INPUT,
		EnableTranscoding: proto.Bool(true),
		State:             &livekit.IngressState{ResourceId: resourceID},
	}
}

func singleLayerInfo(resourceID string) *livekit.IngressInfo {
	info := rtmpInfo(resourceID)
	info.InputType = livekit.IngressInput_URL_INPUT
	info.Video = &livekit.IngressVideoOptions{
		EncodingOptions: &livekit.IngressVideoOptions_Options{
			Options: &livekit.IngressVideoEncodingOptions{
				VideoCodec: livekit.VideoCodec_H264_BASELINE,
				Layers:     []*livekit.VideoLayer{{Quality: livekit.VideoQuality_HIGH, Width: 1280, Height: 720}},
			},
		},
	}
	return info
}

func acceptOnGPU(t *testing.T, m *Monitor, info *livekit.IngressInfo, encoders int32) {
	t.Helper()
	accept, gpuEncoders := m.AcceptIngress(info)
	require.True(t, accept)
	require.Equal(t, encoders, gpuEncoders)
	m.IngressStarted(info, gpuEncoders)
}

func TestGPUAdmissionSpillsOverToTheCPU(t *testing.T) {
	m := gpuTestMonitor(t, "  enabled: true\n  max_encoder_sessions: 7\n  cpu_cost: 0.0001\n")

	acceptOnGPU(t, m, rtmpInfo("a"), 3)
	acceptOnGPU(t, m, rtmpInfo("b"), 3)
	require.Equal(t, int32(6), m.gpuInUse())

	spilled := rtmpInfo("c")
	accept, gpuEncoders := m.AcceptIngress(spilled)
	require.True(t, accept, "a session that does not fit on the gpu is accepted on the cpu")
	require.Zero(t, gpuEncoders)
	m.IngressStarted(spilled, gpuEncoders)
	require.Equal(t, int32(6), m.gpuInUse(), "a spilled session holds no encoder sessions")
	require.True(t, m.CanAccept(), "the node stays available through the cpu")
	require.True(t, m.CanAcceptIngress(rtmpInfo("d")))

	acceptOnGPU(t, m, singleLayerInfo("e"), 1)
	require.Equal(t, int32(7), m.gpuInUse(), "a single layer session fits the remaining capacity")

	whipBypass := &livekit.IngressInfo{InputType: livekit.IngressInput_WHIP_INPUT, EnableTranscoding: proto.Bool(false), State: &livekit.IngressState{ResourceId: "w"}}
	vp8 := singleLayerInfo("v")
	vp8.Video.GetOptions().VideoCodec = livekit.VideoCodec_VP8
	acceptOnGPU(t, m, whipBypass, 0)
	acceptOnGPU(t, m, vp8, 0)
	require.Equal(t, int32(7), m.gpuInUse(), "bypassed whip and vp8 are not counted")

	for _, info := range []*livekit.IngressInfo{spilled, whipBypass, vp8} {
		m.IngressEnded(info)
	}
	require.Equal(t, int32(7), m.gpuInUse(), "ending cpu sessions releases no encoder sessions")

	m.IngressEnded(rtmpInfo("a"))
	require.Equal(t, int32(4), m.gpuInUse(), "release is keyed by resource id")
	acceptOnGPU(t, m, rtmpInfo("f"), 3)
	for _, id := range []string{"b", "e", "f"} {
		m.IngressEnded(rtmpInfo(id))
	}
	require.Zero(t, m.gpuInUse(), "counters balance")
}

func TestGPUAdmissionReleasesAnUnstartedReservation(t *testing.T) {
	m := gpuTestMonitor(t, "  enabled: true\n  max_encoder_sessions: 3\n  cpu_cost: 0.0001\n")

	accept, gpuEncoders := m.AcceptIngress(rtmpInfo("a"))
	require.True(t, accept)
	require.Equal(t, int32(3), gpuEncoders)
	require.Equal(t, int32(3), m.gpuInUse(), "the reservation holds the sessions before start")
	require.Eventually(t, func() bool { return m.gpuInUse() == 0 }, 3*time.Second, 50*time.Millisecond, "a session that never starts releases its reservation")
}

func TestGPUDisabledAdmissionIsUnchanged(t *testing.T) {
	m := gpuTestMonitor(t, "  max_encoder_sessions: 1\n")

	for i := range 3 {
		info := rtmpInfo(string(rune('a' + i)))
		accept, gpuEncoders := m.AcceptIngress(info)
		require.True(t, accept)
		require.Zero(t, gpuEncoders)
		m.IngressStarted(info, gpuEncoders)
	}
	require.Zero(t, m.gpuInUse())
	require.True(t, m.CanAccept())
}
