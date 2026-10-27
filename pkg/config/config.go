// Copyright 2023 LiveKit, Inc.
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
	"os"
	"slices"
	"strings"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"

	"github.com/livekit/mediatransportutil/pkg/rtcconfig"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/medialogutils"
	"github.com/livekit/protocol/logger/zaputil"
	"github.com/livekit/protocol/redis"
	"github.com/livekit/protocol/rpc"
	"github.com/livekit/protocol/utils"
	"github.com/livekit/psrpc"
	lksdk "github.com/livekit/server-sdk-go/v2"

	"github.com/livekit/ingress/pkg/errors"
)

const (
	DefaultRTMPPort      = 1935
	DefaultWHIPPort      = 8080
	DefaultHTTPRelayPort = 9090

	// NVENC encoder sessions per node. Consumer GeForce cards cap concurrent NVENC sessions
	// (8 on current drivers, 3 or 5 on older ones), datacenter cards do not.
	DefaultGPUMaxEncoderSessions = 8
	DefaultGPUCPUCost            = 0.5
	DefaultGPUPreset             = "p3"
)

// GPUTier is a video transcoding chain. Tiers order from the most GPU work to none.
type GPUTier string

const (
	// GPUTierNvidia uploads frames once and converts, scales and encodes on the GPU.
	GPUTierNvidia GPUTier = "nvidia"
	// GPUTierNvidiaEncode converts and scales on the CPU and encodes with NVENC.
	// It is the tier for images or drivers without NVRTC, which the CUDA converters need.
	GPUTierNvidiaEncode GPUTier = "nvidia-encode"
	GPUTierCPU          GPUTier = "cpu"
)

// UsesGPU returns true if the tier holds NVENC encoder sessions.
func (t GPUTier) UsesGPU() bool {
	return t == GPUTierNvidia || t == GPUTierNvidiaEncode
}

// Next returns the tier a session falls back to when this one fails.
func (t GPUTier) Next() GPUTier {
	if t == GPUTierNvidia {
		return GPUTierNvidiaEncode
	}
	return GPUTierCPU
}

// GPUPresets are the nvh264enc presets from fastest to slowest.
var GPUPresets = []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7"}

var (
	DefaultICEPortRange = []uint16{2000, 4000}
)

type Config struct {
	*ServiceConfig  `yaml:",inline"`
	*InternalConfig `yaml:",inline"`
}

type ServiceConfig struct {
	Redis     *redis.RedisConfig `yaml:"redis"`      // required
	ApiKey    string             `yaml:"api_key"`    // required (env LIVEKIT_API_KEY)
	ApiSecret string             `yaml:"api_secret"` // required (env LIVEKIT_API_SECRET)
	WsUrl     string             `yaml:"ws_url"`     // required (env LIVEKIT_WS_URL)

	HealthPort       int             `yaml:"health_port"`
	DebugHandlerPort int             `yaml:"debug_handler_port"`
	PrometheusPort   int             `yaml:"prometheus_port"`
	RTMPPort         int             `yaml:"rtmp_port"` // -1 to disable RTMP
	WHIPPort         int             `yaml:"whip_port"` // -1 to disable WHIP
	HTTPRelayPort    int             `yaml:"http_relay_port"`
	Logging          logger.Config   `yaml:"logging"`
	Development      bool            `yaml:"development"`
	PSRPCSkipClaim   bool            `yaml:"psrpc_skip_claim,omitempty"` // Lets psrpc servers skip the claim handshake on queue rpcs
	PSRPC            rpc.PSRPCConfig `yaml:"psrpc,omitempty"`
	// Allow URL pull ingresses to pull from udp:// urls. Disabled by default, and should only be
	// enabled on deployments where both the API callers and the network the handlers run on are trusted.
	// Unlike the http and srt sources, udpsrc doesn't connect out to the url host: it binds a local
	// socket on the address and port taken from the caller provided url, and joins the multicast group
	// if one is given. This has a few consequences:
	//   - The caller controls which local port the handler binds, and can collide with other services
	//     running on the host.
	//   - UDP is connectionless and unauthenticated, so any host able to reach that port can inject
	//     media into the session, or spoof the sender address to disrupt a legitimate feed.
	//   - The caller can make the handler join arbitrary multicast groups and republish whatever
	//     traffic it receives into a LiveKit room, turning the ingress into a relay for streams on
	//     the handler's local network that the caller couldn't otherwise reach.
	EnableUDPURLPull bool `yaml:"enable_udp_url_pull,omitempty"`
	// Network interface to join multicast groups on for UDP url pull. Empty means let the OS decide.
	MulticastInterface string `yaml:"multicast_interface,omitempty"`

	// Used for WHIP transport
	RTCConfig rtcconfig.RTCConfig `yaml:"rtc_config"`

	// CPU costs for various ingress types
	CPUCost CPUCostConfig `yaml:"cpu_cost"`

	// NVIDIA GPU video encoding for H.264 transcoding
	GPU GPUConfig `yaml:"gpu"`

	// Experimental config
	// Reduces ingest e2e latency by dropping excess preroll buffers
	EnableStreamLatencyReduction bool `yaml:"enable_stream_latency_reduction"`

	// LoggerTee duplicates the log stream InitLogger builds. Set it before
	// Init; the zero value is a no-op.
	LoggerTee zaputil.Tee `yaml:"-"`
}

type InternalConfig struct {
	// internal
	ServiceName string `yaml:"service_name"`
	NodeID      string `yaml:"node_id"` // Do not provide, will be overwritten
}

type CPUCostConfig struct {
	RTMPCpuCost                  float64 `yaml:"rtmp_cpu_cost"`
	WHIPCpuCost                  float64 `yaml:"whip_cpu_cost"`
	WHIPBypassTranscodingCpuCost float64 `yaml:"whip_bypass_transcoding_cpu_cost"`
	URLCpuCost                   float64 `yaml:"url_cpu_cost"`
	MinIdleRatio                 float64 `yaml:"min_idle_ratio"` // Target idle cpu ratio when deciding availability for new requests
}

type GPUConfig struct {
	Enabled bool `yaml:"enabled"`
	// NVENC encoder sessions the node admits at once. A stream holds one per simulcast layer.
	MaxEncoderSessions int `yaml:"max_encoder_sessions"`
	// CPU cores reserved per GPU session for demuxing, audio and relaying
	CPUCost float64 `yaml:"cpu_cost"`
	// nvh264enc preset, p1 (fastest) to p7 (slowest)
	Preset string `yaml:"preset"`
	// Tier is resolved by the service's startup probe, not set by hand. A handler reads it from
	// the config the service hands it, set to cpu when the session spilled over to the CPU.
	Tier GPUTier `yaml:"tier,omitempty"`
}

// Encodes returns true if the GPU encodes the video codec. VP8 has no NVENC support.
func (g GPUConfig) Encodes(codec livekit.VideoCodec) bool {
	return g.Enabled && g.Tier.UsesGPU() && codec == livekit.VideoCodec_H264_BASELINE
}

func (g *GPUConfig) initDefaults() error {
	if g.MaxEncoderSessions <= 0 {
		g.MaxEncoderSessions = DefaultGPUMaxEncoderSessions
		logger.Infow("gpu max_encoder_sessions not set, using default", "maxEncoderSessions", g.MaxEncoderSessions)
	}
	if g.CPUCost <= 0 {
		g.CPUCost = DefaultGPUCPUCost
		logger.Infow("gpu cpu_cost not set, using default", "cpuCost", g.CPUCost)
	}
	if g.Preset == "" {
		g.Preset = DefaultGPUPreset
	}
	if !slices.Contains(GPUPresets, g.Preset) {
		return psrpc.NewErrorf(psrpc.InvalidArgument, "gpu preset %q is not one of %s", g.Preset, strings.Join(GPUPresets, ", "))
	}
	if g.Tier == "" {
		g.Tier = GPUTierNvidia
	}
	return nil
}

func NewConfig(confString string) (*Config, error) {
	conf := &Config{
		ServiceConfig: &ServiceConfig{
			ApiKey:    os.Getenv("LIVEKIT_API_KEY"),
			ApiSecret: os.Getenv("LIVEKIT_API_SECRET"),
			WsUrl:     os.Getenv("LIVEKIT_WS_URL"),
			PSRPC:     rpc.DefaultPSRPCConfig,
		},
		InternalConfig: &InternalConfig{
			ServiceName: "ingress",
		},
	}
	if confString != "" {
		if err := yaml.Unmarshal([]byte(confString), conf); err != nil {
			return nil, errors.ErrCouldNotParseConfig(err)
		}
	}

	if conf.Redis == nil {
		return nil, psrpc.NewErrorf(psrpc.InvalidArgument, "redis configuration is required")
	}

	return conf, nil
}

// SkipClaimEnabled gates psrpc.WithServerSkipClaim on the ingress rpc servers.
func (c *ServiceConfig) SkipClaimEnabled() bool {
	return c.PSRPCSkipClaim
}

func (c *ServiceConfig) InitDefaults() error {
	if c.RTMPPort == 0 {
		c.RTMPPort = DefaultRTMPPort
	}
	if c.HTTPRelayPort == 0 {
		c.HTTPRelayPort = DefaultHTTPRelayPort
	}
	if c.WHIPPort == 0 {
		c.WHIPPort = DefaultWHIPPort
	}
	if c.GPU.Enabled {
		if err := c.GPU.initDefaults(); err != nil {
			return err
		}
	}

	err := c.InitWhipConf()
	if err != nil {
		return err
	}

	return nil
}

func (c *ServiceConfig) InitWhipConf() error {
	if c.WHIPPort <= 0 {
		return nil
	}

	if c.RTCConfig.UDPPort.Start == 0 && c.RTCConfig.ICEPortRangeStart == 0 {
		c.RTCConfig.UDPPort.Start = 7885
	}

	// Validate will set the NodeIP
	err := c.RTCConfig.Validate(c.Development)
	if err != nil {
		return err
	}

	return nil
}

func (c *Config) Init() error {
	c.NodeID = utils.NewGuid("NE_")

	err := c.InitDefaults()
	if err != nil {
		return err
	}

	return c.InitLogger()
}

func (c *Config) InitLogger(values ...interface{}) error {
	zl, err := logger.NewZapLogger(&c.Logging, logger.WithTee(c.LoggerTee))
	if err != nil {
		return err
	}

	values = append(c.getLoggerValues(), values...)
	l := zl.WithValues(values...)
	logger.SetLogger(l, c.ServiceName)
	lksdk.SetLogger(medialogutils.NewOverrideLogger(nil))

	return nil
}

// To use with zap logger
func (c *Config) getLoggerValues() []interface{} {
	return []interface{}{"nodeID", c.NodeID}
}

// GetLoggerFields - to use with logrus
func (c *Config) GetLoggerFields() logrus.Fields {
	fields := logrus.Fields{
		"logger": c.ServiceName,
	}
	v := c.getLoggerValues()
	for i := 0; i < len(v); i += 2 {
		fields[v[i].(string)] = v[i+1]
	}

	return fields
}
