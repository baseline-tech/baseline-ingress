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
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/ingress/pkg/config"
	"github.com/livekit/protocol/logger"
)

// ProbeGPU turns gpu.enabled off when the nvcodec elements are not registered, so sessions fall back to the CPU.
func ProbeGPU(gpu *config.GPUConfig) {
	if !gpu.Enabled {
		return
	}

	gst.Init(nil)

	var missing []string
	for _, name := range nvidiaElementFactories {
		if gst.Find(name) == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		logger.Warnw("gpu transcoding unavailable, using the cpu", nil, "missingElements", missing)
		gpu.Enabled = false
		return
	}

	logger.Infow("gpu transcoding enabled", "maxSessions", gpu.MaxSessions, "cpuCost", gpu.CPUCost)
}
