# Deploying baseline-ingress

This guide runs the baseline fork of LiveKit Ingress on an NVIDIA GPU host, next to an existing
LiveKit server. The fork speaks the same Redis RPC protocol as upstream Ingress. Nothing changes
on the LiveKit server side beyond the usual `ingress` section.

The image is `ghcr.io/baseline-tech/baseline-ingress`. Each release is tagged
`v<upstream>-gpu.<n>` (for example `v1.5.0-gpu.1`), plus `latest` and `sha-<commit>`. Pin a
version tag in production. Releases are cut with the `/baseline-release` Claude skill, see
[.claude/skills/baseline-release/SKILL.md](.claude/skills/baseline-release/SKILL.md).

## How the pieces connect

```
 OBS / encoder ──RTMP 1935──▶ ingress ──WebRTC──▶ livekit-server ──▶ viewers
                              │    ▲
                              ▼    │ RPC (create, start, state updates)
                              redis ◀──────────── livekit-server
```

- The ingress and the LiveKit server must use the **same Redis**. That is the only control channel.
- The ingress joins rooms as a normal participant through `ws_url`, using `api_key`/`api_secret`.
- The ingress host must reach the LiveKit server's signal port (7880 by default) and its WebRTC
  ports (UDP range and TCP fallback), like any other client.

## 1. Prepare the GPU host

You need Linux x86_64 with an NVIDIA GPU that has NVENC (any GeForce, T4, L4, A10 and similar).

1. Install the NVIDIA driver. Use **570 or newer**, since the image ships CUDA 12.8 NVRTC. Check it:
   ```sh
   nvidia-smi
   ```
2. Install Docker and the
   [NVIDIA Container Toolkit](https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/latest/install-guide.html),
   then wire it into Docker:
   ```sh
   sudo nvidia-ctk runtime configure --runtime=docker
   sudo systemctl restart docker
   ```
3. Check that containers see the GPU:
   ```sh
   docker run --rm --gpus all ubuntu nvidia-smi
   ```

## 2. Pull the image

The package is private while the repo is private. Create a GitHub classic token with only the
`read:packages` scope, then log in once on each host:

```sh
echo <token> | docker login ghcr.io -u <github-user> --password-stdin
docker pull ghcr.io/baseline-tech/baseline-ingress:v1.5.0-gpu.1
```

Check that the GPU elements load inside the image:

```sh
docker run --rm --gpus all -e NVIDIA_DRIVER_CAPABILITIES=compute,video,utility \
  --entrypoint gst-inspect-1.0 ghcr.io/baseline-tech/baseline-ingress:v1.5.0-gpu.1 \
  nvh264enc cudaupload cudaconvert cudaconvertscale
```

Each element should print its details. "No such element" means the driver capabilities or the
toolkit are not set up, so go back to step 1.

## 3. Configure the LiveKit server

Add an `ingress` section to the LiveKit server config so it can hand out publish URLs. Use the
address your encoders will reach the ingress on (a DNS name or a load balancer).

```yaml
redis:
  address: redis.internal:6379   # the same Redis the ingress uses
ingress:
  rtmp_base_url: rtmp://ingress.example.com:1935/live
  whip_base_url: https://ingress.example.com/w
```

Restart the LiveKit server after the change.

## 4. Configure the ingress

Create `/etc/baseline-ingress/ingress.yaml`:

```yaml
api_key: <livekit api key>
api_secret: <livekit api secret>
ws_url: wss://livekit.example.com      # how the ingress reaches the LiveKit server
redis:
  address: redis.internal:6379
  # username / password / db / use_tls as your Redis needs

rtmp_port: 1935        # -1 disables RTMP
whip_port: 8080        # -1 disables WHIP
http_relay_port: 9090  # internal only, between the service and its handler processes
health_port: 8081      # must not clash with whip_port
prometheus_port: 9100

logging:
  level: info

# CPU budget per session type, in cores. These are the upstream recommendations.
cpu_cost:
  rtmp_cpu_cost: 2
  url_cpu_cost: 2
  whip_cpu_cost: 2
  whip_bypass_transcoding_cpu_cost: 0.1

gpu:
  enabled: true
  # NVENC sessions this node may hold. One stream holds one per simulcast layer (3 by default).
  # GeForce drivers cap this (8 on current drivers). Datacenter cards have no cap, so set it
  # from a benchmark (section 8).
  max_encoder_sessions: 8
  # CPU cores reserved per GPU stream (demux, audio, WebRTC)
  cpu_cost: 0.5
  # nvh264enc preset: p1 is fastest, p7 is best quality
  preset: p3

# Only needed for WHIP: ICE settings, same format as the livekit-server rtc section
# rtc_config:
#   udp_port: 7885
#   use_external_ip: true
```

The ingress also accepts `LIVEKIT_API_KEY`, `LIVEKIT_API_SECRET` and `LIVEKIT_WS_URL` from the
environment if you'd rather keep secrets out of the file.

## 5. Run it

Host networking is the simplest choice, because WHIP needs a UDP port range for ICE:

```sh
docker run -d --name baseline-ingress --restart unless-stopped \
  --network host \
  --gpus all \
  -e NVIDIA_DRIVER_CAPABILITIES=compute,video,utility \
  -e INGRESS_CONFIG_FILE=/config/ingress.yaml \
  -v /etc/baseline-ingress:/config:ro \
  --stop-timeout 600 \
  ghcr.io/baseline-tech/baseline-ingress:v1.5.0-gpu.1
```

The same as Docker Compose:

```yaml
services:
  ingress:
    image: ghcr.io/baseline-tech/baseline-ingress:v1.5.0-gpu.1
    restart: unless-stopped
    network_mode: host
    stop_grace_period: 10m
    environment:
      NVIDIA_DRIVER_CAPABILITIES: compute,video,utility
      INGRESS_CONFIG_FILE: /config/ingress.yaml
    volumes:
      - /etc/baseline-ingress:/config:ro
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu, video, compute, utility]
```

### Ports to open

| Port | Protocol | Who connects | Purpose |
|------|----------|--------------|---------|
| 1935 | TCP | encoders (OBS, ffmpeg) | RTMP publish |
| 8080 | TCP | encoders | WHIP publish (put HTTPS in front of it) |
| `rtc_config` UDP port or range | UDP | encoders | WHIP media |
| 9100 | TCP | your Prometheus | metrics, keep private |
| 8081 | TCP | your load balancer | health check, keep private |
| 9090 | TCP | nobody outside the host | internal relay, do not expose |

## 6. Verify the deployment

1. **Startup log.** `docker logs baseline-ingress` should show
   ```
   gpu transcoding enabled {"tier": "nvidia", "maxEncoderSessions": 8, ...}
   ```
   - `"tier": "nvidia-encode"` means the GPU encodes, but converting and scaling run on the
     CPU. That usually means NVRTC failed to load, so check the driver version.
   - `gpu transcoding unavailable, using the cpu` means no GPU tier worked. The
     `gpu tier failed its smoke encode` lines above it give the reason.
2. **Create an ingress** with the [LiveKit CLI](https://github.com/livekit/livekit-cli):
   ```sh
   cat > ingress.json <<'EOF'
   {"input_type": 0, "name": "test", "room_name": "test-room",
    "participant_identity": "rtmp-test", "enable_transcoding": true}
   EOF
   lk --url https://livekit.example.com --api-key <key> --api-secret <secret> ingress create ingress.json
   ```
   It prints the RTMP URL and stream key.
3. **Publish** from OBS (Server set to the URL, Stream Key set to the key), or with ffmpeg:
   ```sh
   ffmpeg -re -f lavfi -i testsrc2=size=1280x720:rate=30 -f lavfi -i sine \
     -c:v libx264 -preset veryfast -g 60 -c:a aac -f flv rtmp://ingress.example.com:1935/live/<stream key>
   ```
4. **Check the result.**
   - `lk ... ingress list` shows `ENDPOINT_PUBLISHING`.
   - The ingress log has `video layer` lines with `"backend": "nvidia"`.
   - `nvidia-smi dmon -s u` shows load in the `enc` column.
   - Join `test-room` from any LiveKit client and the video plays.

## 7. Monitor it

Scrape `http://<host>:9100/metrics`. Useful series:

- `livekit_ingress_gpu_encoder_sessions` is the number of NVENC sessions held. When it nears
  `max_encoder_sessions`, new streams go to the CPU.
- The upstream CPU and request gauges (`livekit_ingress_*`) are unchanged.

Log lines worth alerting on:

- `gpu encoder sessions exhausted, transcoding on the cpu` means the GPU is full and streams are
  spilling to the CPU.
- `gpu tier failed its smoke encode, falling back` means one stream could not get NVENC and was
  built on a lower tier.

## 8. Size a node

`max_encoder_sessions` and `preset` decide capacity. Measure them on the real card with the
benchmark kit in [scripts/gpu-bench](scripts/gpu-bench/README.md). It runs N synthetic 1080p30
streams per preset and reports GPU encoder load, CPU and frame drops. Set `max_encoder_sessions`
just under the session count where encoder load or drops climb. Remember that each stream holds
3 sessions with the default layers.

To scale out, run more ingress nodes on the same Redis. RTMP and WHIP streams land on whichever
node your load balancer sends the encoder to, so put a TCP load balancer in front of port 1935
(and an HTTPS one in front of WHIP) that uses the health check. URL pull ingresses are offered
to all nodes over Redis and only a node with spare capacity claims one.

## 9. Upgrade and roll back

```sh
docker pull ghcr.io/baseline-tech/baseline-ingress:<new tag>
# update the tag in the run command or compose file, then
docker stop baseline-ingress   # SIGTERM: stops taking new streams, waits for live ones
docker rm baseline-ingress
# start it again with the new tag
```

`docker stop` sends SIGTERM, which drains the node. Live streams keep running until they end,
which is why the run command sets a long stop timeout. To move streams off a node sooner, take
it out of your RTMP load balancer first. To roll back, start the previous tag the same way.

## Troubleshooting

| Symptom | Likely cause | Fix |
|---------|--------------|-----|
| `could not create element: cudaupload` at startup | Container started without the GPU | Add `--gpus all` and `NVIDIA_DRIVER_CAPABILITIES=compute,video,utility`, check step 1 |
| Tier `nvidia-encode` instead of `nvidia` | NVRTC can't compile CUDA kernels (old driver) | Upgrade the driver to 570+ |
| Streams stay `ENDPOINT_BUFFERING` | Ingress can't reach `ws_url` or the LiveKit WebRTC ports | Check `ws_url`, firewall, and the LiveKit server's `rtc` settings |
| `ingress create` works but publishing is refused | Ingress and LiveKit server use different Redis | Point both at the same Redis address and db |
| `WHIP server start failed ... address already in use` | `health_port` or another service uses `whip_port` | Give each port its own number |
| New streams land on the CPU while the GPU is idle | `max_encoder_sessions` too low for 3-layer streams | Raise it, or lower the layer count in the ingress request |
| Encoder load high but streams stutter | Preset too slow for the stream count | Use a faster preset (lower `p`) or fewer streams per node |
