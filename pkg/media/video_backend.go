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
	"fmt"

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/livekit"
)

type elementSpec struct {
	factory string
	caps    string
}

type videoBackend struct {
	name       string
	converters []elementSpec
	scaler     string
	rawCaps    string

	newH264Encoder func(layer *livekit.VideoLayer, threadCount uint) (*gst.Element, error)
}

var cpuVideoBackend = &videoBackend{
	name:           "cpu",
	converters:     []elementSpec{{factory: "videoconvert"}},
	scaler:         "videoscale",
	rawCaps:        "video/x-raw",
	newH264Encoder: newX264Encoder,
}

var nvidiaVideoBackend = &videoBackend{
	name: "nvidia",
	converters: []elementSpec{
		{factory: "cudaupload"},
		{factory: "cudaconvert"},
		{factory: "capsfilter", caps: "video/x-raw(memory:CUDAMemory),format=NV12"},
	},
	scaler:         "cudaconvertscale",
	rawCaps:        "video/x-raw(memory:CUDAMemory)",
	newH264Encoder: newNvH264Encoder,
}

var nvidiaElementFactories = []string{"cudaupload", "cudaconvert", "cudaconvertscale", "nvh264enc"}

func selectVideoBackend(gpu config.GPUConfig, codec livekit.VideoCodec) *videoBackend {
	if gpu.Encodes(codec) {
		return nvidiaVideoBackend
	}
	return cpuVideoBackend
}

func (b *videoBackend) newConverters() ([]*gst.Element, error) {
	return newElements(b.converters)
}

func (b *videoBackend) newScalers(layer *livekit.VideoLayer) ([]*gst.Element, error) {
	return newElements([]elementSpec{
		{factory: b.scaler},
		{factory: "capsfilter", caps: fmt.Sprintf("%s,width=%d,height=%d", b.rawCaps, layer.Width, layer.Height)},
	})
}

func newElements(specs []elementSpec) ([]*gst.Element, error) {
	elements := make([]*gst.Element, 0, len(specs))
	for _, spec := range specs {
		e, err := gst.NewElement(spec.factory)
		if err != nil {
			return nil, err
		}
		if spec.caps != "" {
			if err = e.SetProperty("caps", gst.NewCapsFromString(spec.caps)); err != nil {
				return nil, err
			}
		}
		elements = append(elements, e)
	}
	return elements, nil
}

func newX264Encoder(layer *livekit.VideoLayer, threadCount uint) (*gst.Element, error) {
	enc, err := gst.NewElement("x264enc")
	if err != nil {
		return nil, err
	}

	if err = enc.SetProperty("bitrate", uint(layer.Bitrate/1000)); err != nil {
		return nil, err
	}
	// 1s VBV buffer size
	if err = enc.SetProperty("vbv-buf-capacity", uint(1000)); err != nil {
		return nil, err
	}
	if err = enc.SetProperty("byte-stream", true); err != nil {
		return nil, err
	}
	if err = enc.SetProperty("threads", threadCount); err != nil {
		return nil, err
	}

	enc.SetArg("tune", "zerolatency")
	enc.SetArg("speed-preset", "veryfast")

	return enc, nil
}

func newNvH264Encoder(layer *livekit.VideoLayer, _ uint) (*gst.Element, error) {
	enc, err := gst.NewElement("nvh264enc")
	if err != nil {
		return nil, err
	}

	// nvh264enc scales bitrate by 1024, not 1000
	if err = enc.SetProperty("bitrate", uint(layer.Bitrate/1024)); err != nil {
		return nil, err
	}
	if err = enc.SetProperty("zerolatency", true); err != nil {
		return nil, err
	}
	if err = enc.SetProperty("repeat-sequence-header", true); err != nil {
		return nil, err
	}
	// defaults to true, x264enc emits none
	if err = enc.SetProperty("aud", false); err != nil {
		return nil, err
	}

	enc.SetArg("rc-mode", "cbr")
	enc.SetArg("tune", "ultra-low-latency")
	enc.SetArg("preset", "p3")

	return enc, nil
}
