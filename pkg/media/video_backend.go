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
	"time"

	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
)

type elementSpec struct {
	factory string
	caps    string
}

type videoBackend struct {
	tier       config.GPUTier
	converters []elementSpec
	scaler     string
	rawCaps    string
	preset     string // nvh264enc preset, bound by withPreset

	newH264Encoder func(preset string, layer *livekit.VideoLayer, threadCount uint) (*gst.Element, error)
}

var cpuVideoBackend = &videoBackend{
	tier:           config.GPUTierCPU,
	converters:     []elementSpec{{factory: "videoconvert"}},
	scaler:         "videoscale",
	rawCaps:        "video/x-raw",
	newH264Encoder: newX264Encoder,
}

var nvidiaVideoBackend = &videoBackend{
	tier: config.GPUTierNvidia,
	converters: []elementSpec{
		{factory: "cudaupload"},
		{factory: "cudaconvert"},
		{factory: "capsfilter", caps: "video/x-raw(memory:CUDAMemory),format=NV12"},
	},
	scaler:         "cudaconvertscale",
	rawCaps:        "video/x-raw(memory:CUDAMemory)",
	newH264Encoder: newNvH264Encoder,
}

var nvidiaEncodeVideoBackend = &videoBackend{
	tier: config.GPUTierNvidiaEncode,
	converters: []elementSpec{
		{factory: "videoconvert"},
		{factory: "capsfilter", caps: "video/x-raw,format=NV12"},
	},
	scaler:         "videoscale",
	rawCaps:        "video/x-raw",
	newH264Encoder: newNvH264Encoder,
}

var videoBackends = map[config.GPUTier]*videoBackend{
	config.GPUTierNvidia:       nvidiaVideoBackend,
	config.GPUTierNvidiaEncode: nvidiaEncodeVideoBackend,
	config.GPUTierCPU:          cpuVideoBackend,
}

// Smoke encode bounds. nvh264enc takes 160x64 and up.
const (
	smokeWidth   = 320
	smokeHeight  = 240
	smokeFrames  = 5
	smokeTimeout = 10 * time.Second
)

type tierFailure struct {
	tier config.GPUTier
	err  error
}

// resolveVideoTier walks down from first and returns the first tier whose H.264 chain encodes a
// few frames with encoders concurrent encoder sessions, plus why each tier above it failed.
// The CPU tier is the floor and is not tested.
func resolveVideoTier(gpu config.GPUConfig, first config.GPUTier, encoders int) (config.GPUTier, []tierFailure) {
	var failures []tierFailure
	for tier := first; tier.UsesGPU(); tier = tier.Next() {
		err := videoBackends[tier].withPreset(gpu.Preset).smokeEncode(encoders)
		if err == nil {
			return tier, failures
		}
		failures = append(failures, tierFailure{tier: tier, err: err})
	}
	return config.GPUTierCPU, failures
}

// selectVideoBackend picks the backend for a stream with encoders simulcast layers. The GPU tier
// in gpu is smoke tested with that many encoder sessions first, and a tier that fails hands the
// whole stream to the next one, down to the CPU, before anything is built.
func selectVideoBackend(gpu config.GPUConfig, codec livekit.VideoCodec, encoders int) *videoBackend {
	if !gpu.Encodes(codec) {
		return cpuVideoBackend
	}
	tier, failures := resolveVideoTier(gpu, gpu.Tier, encoders)
	for _, f := range failures {
		logger.Warnw("gpu tier failed its smoke encode, falling back", f.err, "tier", f.tier, "next", f.tier.Next(), "encoders", encoders)
	}
	return videoBackends[tier].withPreset(gpu.Preset)
}

func (b *videoBackend) withPreset(preset string) *videoBackend {
	if preset == "" || preset == b.preset {
		return b
	}
	c := *b
	c.preset = preset
	return &c
}

func (b *videoBackend) h264Encoder(layer *livekit.VideoLayer, threadCount uint) (*gst.Element, error) {
	return b.newH264Encoder(b.preset, layer, threadCount)
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

// smokeEncode runs smokeFrames frames through the backend's real H.264 chain, fanned out to
// encoders encoder sessions, into fakesinks, and returns the first pipeline error.
func (b *videoBackend) smokeEncode(encoders int) error {
	pipeline, err := gst.NewPipeline("")
	if err != nil {
		return err
	}
	defer func() { _ = pipeline.SetState(gst.StateNull) }()

	layer := &livekit.VideoLayer{Quality: livekit.VideoQuality_HIGH, Width: smokeWidth, Height: smokeHeight, Bitrate: 500_000}

	head, err := newElements(append([]elementSpec{
		{factory: "videotestsrc"},
		{factory: "capsfilter", caps: fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=30/1", smokeWidth, smokeHeight)},
	}, b.converters...))
	if err != nil {
		return err
	}
	if err = head[0].SetProperty("num-buffers", smokeFrames); err != nil {
		return err
	}
	tee, err := gst.NewElement("tee")
	if err != nil {
		return err
	}
	head = append(head, tee)
	if err = pipeline.AddMany(head...); err != nil {
		return err
	}
	if err = gst.ElementLinkMany(head...); err != nil {
		return err
	}

	for i := 0; i < encoders; i++ {
		branch, err := newElements([]elementSpec{{factory: "queue"}})
		if err != nil {
			return err
		}
		scalers, err := b.newScalers(layer)
		if err != nil {
			return err
		}
		enc, err := b.h264Encoder(layer, 1)
		if err != nil {
			return err
		}
		sink, err := gst.NewElement("fakesink")
		if err != nil {
			return err
		}
		branch = append(branch, scalers...)
		branch = append(branch, enc, sink)
		if err = pipeline.AddMany(branch...); err != nil {
			return err
		}
		if err = gst.ElementLinkMany(append([]*gst.Element{tee}, branch...)...); err != nil {
			return err
		}
	}

	if err = pipeline.SetState(gst.StatePlaying); err != nil {
		return err
	}

	msg := pipeline.GetPipelineBus().TimedPopFiltered(gst.ClockTime(smokeTimeout), gst.MessageEOS|gst.MessageError)
	switch {
	case msg == nil:
		return fmt.Errorf("no EOS after %s", smokeTimeout)
	case msg.Type() == gst.MessageError:
		gerr := msg.ParseError()
		return fmt.Errorf("%s (%s)", gerr.Error(), gerr.DebugString())
	}
	return nil
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

func newX264Encoder(_ string, layer *livekit.VideoLayer, threadCount uint) (*gst.Element, error) {
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

func newNvH264Encoder(preset string, layer *livekit.VideoLayer, _ uint) (*gst.Element, error) {
	enc, err := gst.NewElement("nvh264enc")
	if err != nil {
		return nil, err
	}

	// nvh264enc takes kbit/s and multiplies by 1024, x264enc by 1000
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

	if preset == "" {
		preset = config.DefaultGPUPreset
	}
	enc.SetArg("rc-mode", "cbr")
	enc.SetArg("tune", "ultra-low-latency")
	enc.SetArg("preset", preset)

	return enc, nil
}
