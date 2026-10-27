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

func TestGPUAdmission(t *testing.T) {
	conf, err := config.NewConfig(`
redis:
  address: localhost:6379
ws_url: ws://localhost:7880
api_key: key
api_secret: secret
cpu_cost:
  min_idle_ratio: 0.01
  rtmp_cpu_cost: 0.0001
  url_cpu_cost: 0.0001
gpu:
  enabled: true
  max_sessions: 2
  cpu_cost: 0.0001
`)
	require.NoError(t, err)

	m := NewMonitor()
	require.NoError(t, m.Start(conf))
	t.Cleanup(m.Stop)

	rtmp := func() *livekit.IngressInfo {
		return &livekit.IngressInfo{InputType: livekit.IngressInput_RTMP_INPUT, EnableTranscoding: proto.Bool(true)}
	}
	whipBypass := &livekit.IngressInfo{InputType: livekit.IngressInput_WHIP_INPUT, EnableTranscoding: proto.Bool(false)}
	vp8 := &livekit.IngressInfo{
		InputType:         livekit.IngressInput_URL_INPUT,
		EnableTranscoding: proto.Bool(true),
		Video: &livekit.IngressVideoOptions{
			EncodingOptions: &livekit.IngressVideoOptions_Options{
				Options: &livekit.IngressVideoEncodingOptions{VideoCodec: livekit.VideoCodec_VP8},
			},
		},
	}

	first, second := rtmp(), rtmp()
	require.Eventually(t, m.CanAccept, 5*time.Second, 50*time.Millisecond, "the cpu sampler reports idle time")
	require.True(t, m.AcceptIngress(first))
	require.True(t, m.AcceptIngress(second))
	require.False(t, m.AcceptIngress(rtmp()), "a third session does not fit in max_sessions")
	require.False(t, m.CanAccept(), "a node without a free gpu session is not available")

	m.IngressStarted(first)
	m.IngressStarted(second)
	require.Equal(t, int32(2), m.gpuSessions.Load())
	require.False(t, m.CanAcceptIngress(rtmp()))

	require.True(t, m.CanAcceptIngress(whipBypass), "bypassed whip does not use the gpu")
	require.True(t, m.CanAcceptIngress(vp8), "vp8 is encoded on the cpu")
	m.IngressStarted(vp8)
	m.IngressStarted(whipBypass)
	require.Equal(t, int32(2), m.gpuSessions.Load(), "only gpu transcoded sessions are counted")
	m.IngressEnded(vp8)
	m.IngressEnded(whipBypass)
	require.Equal(t, int32(2), m.gpuSessions.Load())

	m.IngressEnded(first)
	require.Equal(t, int32(1), m.gpuSessions.Load())
	require.Eventually(t, func() bool { return m.CanAcceptIngress(rtmp()) && m.CanAccept() },
		3*time.Second, 50*time.Millisecond, "an ended session frees its gpu session once reservations expire")
}

func TestGPUDisabledAdmissionIsUnchanged(t *testing.T) {
	conf, err := config.NewConfig(`
redis:
  address: localhost:6379
ws_url: ws://localhost:7880
api_key: key
api_secret: secret
cpu_cost:
  min_idle_ratio: 0.01
  rtmp_cpu_cost: 0.0001
gpu:
  max_sessions: 1
`)
	require.NoError(t, err)

	m := NewMonitor()
	require.NoError(t, m.Start(conf))
	t.Cleanup(m.Stop)

	info := &livekit.IngressInfo{InputType: livekit.IngressInput_RTMP_INPUT, EnableTranscoding: proto.Bool(true)}
	require.Eventually(t, func() bool { return m.CanAcceptIngress(info) }, 5*time.Second, 50*time.Millisecond, "the cpu sampler reports idle time")
	for range 3 {
		require.True(t, m.CanAcceptIngress(info))
		m.IngressStarted(info)
	}
	require.Zero(t, m.gpuSessions.Load())
}
