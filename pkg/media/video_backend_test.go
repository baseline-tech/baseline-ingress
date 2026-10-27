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

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/ingress/pkg/stats"
	"github.com/livekit/ingress/pkg/utils"
	"github.com/livekit/protocol/livekit"
)

func TestSelectVideoBackendWithoutGPU(t *testing.T) {
	gst.Init(nil)

	require.Same(t, cpuVideoBackend, selectVideoBackend(config.GPUConfig{}, livekit.VideoCodec_H264_BASELINE, 3))
	require.Same(t, cpuVideoBackend, selectVideoBackend(config.GPUConfig{}, livekit.VideoCodec_VP8, 3))
	require.Same(t, cpuVideoBackend, selectVideoBackend(config.GPUConfig{Enabled: true, Tier: config.GPUTierCPU}, livekit.VideoCodec_H264_BASELINE, 3), "a session spilled over to the cpu")
	require.Same(t, cpuVideoBackend, selectVideoBackend(config.GPUConfig{Enabled: true, Tier: config.GPUTierNvidia}, livekit.VideoCodec_VP8, 3))
}

func TestSelectVideoBackendFallsBackWithoutNvcodec(t *testing.T) {
	requireNoNvidiaEncoder(t)

	gpu := config.GPUConfig{Enabled: true, Preset: "p3", Tier: config.GPUTierNvidia}
	require.Equal(t, config.GPUTierCPU, selectVideoBackend(gpu, livekit.VideoCodec_H264_BASELINE, 3).tier)

	tier, failures := resolveVideoTier(gpu, config.GPUTierNvidia, 3)
	require.Equal(t, config.GPUTierCPU, tier)
	require.Len(t, failures, 2, "both gpu tiers are tried before the cpu")
	require.Equal(t, config.GPUTierNvidia, failures[0].tier)
	require.Equal(t, config.GPUTierNvidiaEncode, failures[1].tier)
	for _, f := range failures {
		t.Logf("%s: %v", f.tier, f.err)
	}
}

func TestCPUSmokeEncode(t *testing.T) {
	gst.Init(nil)

	require.NoError(t, cpuVideoBackend.smokeEncode(3), "the smoke harness encodes with three concurrent x264 sessions")
}

func TestSmokeEncodeReportsPipelineErrors(t *testing.T) {
	gst.Init(nil)

	broken := *cpuVideoBackend
	broken.converters = []elementSpec{{factory: "capsfilter", caps: "video/x-raw,format=NV12"}}
	broken.rawCaps = "video/x-raw,format=I420"
	require.Error(t, broken.smokeEncode(1), "a chain that cannot negotiate fails the smoke instead of hanging")
}

func TestWithPreset(t *testing.T) {
	require.Same(t, nvidiaVideoBackend, nvidiaVideoBackend.withPreset(""))
	bound := nvidiaVideoBackend.withPreset("p7")
	require.NotSame(t, nvidiaVideoBackend, bound)
	require.Equal(t, "p7", bound.preset)
	require.Empty(t, nvidiaVideoBackend.preset, "the shared backend is not mutated")
	require.Same(t, bound, bound.withPreset("p7"))
}

func factoryNames(elements []*gst.Element) []string {
	names := make([]string, 0, len(elements))
	for _, e := range elements {
		names = append(names, e.GetFactory().GetName())
	}
	return names
}

func newTestVideoOutput(t *testing.T, backend *videoBackend, codec livekit.VideoCodec) *VideoOutput {
	t.Helper()
	gst.Init(nil)

	layer := &livekit.VideoLayer{Quality: livekit.VideoQuality_HIGH, Width: 1280, Height: 720, Bitrate: 2_000_000}
	out, err := NewVideoOutput(backend, codec, layer, utils.NewOutputSynchronizer().AddTrack(), func() bool { return false }, stats.NewLocalMediaStatsGatherer(), nil)
	require.NoError(t, err)
	t.Cleanup(func() { out.Close() })
	return out
}

func TestCPUVideoOutputKeepsTheX264Chain(t *testing.T) {
	out := newTestVideoOutput(t, cpuVideoBackend, livekit.VideoCodec_H264_BASELINE)

	require.Equal(t,
		[]string{"queue", "videoscale", "capsfilter", "queue", "x264enc", "capsfilter", "queue", "appsink"},
		factoryNames(out.elements),
	)

	bitrate, err := out.enc.GetProperty("bitrate")
	require.NoError(t, err)
	require.Equal(t, uint(2000), bitrate)
}

func TestCPUVideoOutputVP8(t *testing.T) {
	out := newTestVideoOutput(t, cpuVideoBackend, livekit.VideoCodec_VP8)

	require.Equal(t,
		[]string{"queue", "videoscale", "capsfilter", "queue", "vp8enc", "queue", "appsink"},
		factoryNames(out.elements),
	)
}

func TestCPUConverters(t *testing.T) {
	gst.Init(nil)

	elements, err := cpuVideoBackend.newConverters()
	require.NoError(t, err)
	require.Equal(t, []string{"videoconvert"}, factoryNames(elements))
}

func requireNvidiaEncoder(t *testing.T) {
	t.Helper()
	gst.Init(nil)
	if gst.Find("nvh264enc") == nil {
		t.Skip("nvh264enc is not installed")
	}
}

func requireNoNvidiaEncoder(t *testing.T) {
	t.Helper()
	gst.Init(nil)
	if gst.Find("nvh264enc") != nil {
		t.Skip("nvh264enc is installed")
	}
}

func TestNvidiaEncodeVideoOutput(t *testing.T) {
	requireNvidiaEncoder(t)

	out := newTestVideoOutput(t, nvidiaEncodeVideoBackend.withPreset("p5"), livekit.VideoCodec_H264_BASELINE)

	require.Equal(t,
		[]string{"queue", "videoscale", "capsfilter", "queue", "nvh264enc", "capsfilter", "queue", "appsink"},
		factoryNames(out.elements),
	)

	bitrate, err := out.enc.GetProperty("bitrate")
	require.NoError(t, err)
	require.Equal(t, uint(2_000_000/1024), bitrate)

	converters, err := nvidiaEncodeVideoBackend.newConverters()
	require.NoError(t, err)
	require.Equal(t, []string{"videoconvert", "capsfilter"}, factoryNames(converters))

	require.NoError(t, nvidiaEncodeVideoBackend.smokeEncode(3))
}

func TestNvidiaVideoOutput(t *testing.T) {
	requireNvidiaEncoder(t)
	if gst.Find("cudaconvertscale") == nil {
		t.Skip("cudaconvertscale is not installed, NVRTC is missing")
	}

	out := newTestVideoOutput(t, nvidiaVideoBackend, livekit.VideoCodec_H264_BASELINE)

	require.Equal(t,
		[]string{"queue", "cudaconvertscale", "capsfilter", "queue", "nvh264enc", "capsfilter", "queue", "appsink"},
		factoryNames(out.elements),
	)

	converters, err := nvidiaVideoBackend.newConverters()
	require.NoError(t, err)
	require.Equal(t, []string{"cudaupload", "cudaconvert", "capsfilter"}, factoryNames(converters))

	require.NoError(t, nvidiaVideoBackend.smokeEncode(3))
}
