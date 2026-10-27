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

func TestSelectVideoBackend(t *testing.T) {
	gpu := config.GPUConfig{Enabled: true}

	require.Same(t, nvidiaVideoBackend, selectVideoBackend(gpu, livekit.VideoCodec_H264_BASELINE))
	require.Same(t, cpuVideoBackend, selectVideoBackend(gpu, livekit.VideoCodec_VP8))
	require.Same(t, cpuVideoBackend, selectVideoBackend(config.GPUConfig{}, livekit.VideoCodec_H264_BASELINE))
	require.Same(t, cpuVideoBackend, selectVideoBackend(config.GPUConfig{}, livekit.VideoCodec_VP8))
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

func requireNvidiaElements(t *testing.T) {
	t.Helper()
	gst.Init(nil)
	for _, name := range nvidiaElementFactories {
		if gst.Find(name) == nil {
			t.Skipf("%s is not installed", name)
		}
	}
}

func TestNvidiaVideoOutput(t *testing.T) {
	requireNvidiaElements(t)

	out := newTestVideoOutput(t, nvidiaVideoBackend, livekit.VideoCodec_H264_BASELINE)

	require.Equal(t,
		[]string{"queue", "cudaconvertscale", "capsfilter", "queue", "nvh264enc", "capsfilter", "queue", "appsink"},
		factoryNames(out.elements),
	)

	bitrate, err := out.enc.GetProperty("bitrate")
	require.NoError(t, err)
	require.Equal(t, uint(2_000_000/1024), bitrate)

	converters, err := nvidiaVideoBackend.newConverters()
	require.NoError(t, err)
	require.Equal(t, []string{"cudaupload", "cudaconvert", "capsfilter"}, factoryNames(converters))
}
