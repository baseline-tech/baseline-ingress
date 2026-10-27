# GPU transcoding benchmark kit

Measures how many RTMP streams one NVIDIA GPU transcodes through the ingress, per NVENC preset, and what the CPU path would have cost. It runs a local LiveKit server, Redis and the ingress built from this repo, then publishes synthetic 1080p30 streams into it.

## Prerequisites

- A Linux host with an NVIDIA GPU (NVENC), the NVIDIA driver, and the NVIDIA container toolkit.
- Docker with compose v2.
- `lk` (livekit-cli) and `jq`.
- `gst-launch-1.0` with the x264, flv, rtmp2 and an AAC encoder (`avenc_aac` or `voaacenc`) plugins, and `gst-inspect-1.0`.
- `nvidia-smi`.
- Optional, for `quality.sh`: `ffmpeg`, ideally with `libvmaf` (it falls back to PSNR), plus `nvh264enc` in the local GStreamer.

## Setup

Build the ingress image with the GStreamer version this repo pins. `run-bench.sh` does this export for you when `GSTVERSION` is unset.

```sh
cd scripts/gpu-bench
GSTVERSION=$(cat ../../.gst-version) docker compose build
```

`ingress.yaml` and `livekit.yaml` use the dev key `devkey`/`secret`. The image needs `libnvrtc` for the GPU path, see the main README.

## Scripts

- `run-bench.sh N [PRESET] [DURATION_S]` starts the stack, creates N RTMP ingresses (rooms `bench-0` to `bench-N-1`, H.264 720p with three layers), and publishes N 1080p30 test streams for DURATION_S seconds (default 60). When PRESET is given it rewrites `gpu.preset` in `ingress.yaml` and recreates the ingress container (`docker compose up -d --force-recreate ingress`) before publishing. Without it the preset already in the file is used. Ingresses are deleted and publishers killed on exit, including on error or Ctrl-C.
- `sweep.sh` runs `run-bench.sh` for every combination of `PRESETS` (default `p1 p3 p5 p7`) and `NS` (default `1 2 4 8`) with `DURATION` seconds each (default 60) and collects the rows in one table.
- `quality.sh [CLIP]` encodes a 10 s, 720p reference (Big Buck Bunny is downloaded if no CLIP is given) with `nvh264enc` at each preset and with the CPU `x264enc` settings the ingress uses, all at 1700 kbit, and scores each against the source.

## Output

Everything lands in `out/`, which this directory's `.gitignore` excludes.

- `out/<run>/summary.md`, the table for one run, plus `dmon.log` (`nvidia-smi dmon -s pucm -d 1`), `cpu.log` (epoch seconds and ingress container CPU %, every 2 s), `ingress.log` (ingress logs since the run started), `pub-<i>.log` and `pub-<i>.status`.
- `out/sweep-<timestamp>.md`, the combined table from `sweep.sh`.
- `out/quality-<timestamp>/quality.md`, preset against VMAF (or PSNR) and encode wall time.

## Reading the summary

| column | meaning |
|---|---|
| backend | encoder each session reported in its `video layer` log line (`backend=...`), with a count. A mix, or the CPU backend on a GPU run, means sessions fell back. |
| enc % | NVENC engine utilisation from `dmon`. Near 100 means the encoder is the limit. |
| gpu % | SM utilisation from `dmon`, which covers the scale and colour conversion kernels. |
| ingress cpu % | `docker stats` for the ingress container. 100 is one core, so it can exceed 100. |
| too-slow/drop | Count of `too slow`, `frame drop`, `dropped` and `QoS` log lines. Anything above zero means that N is past what the card sustains at that preset. |
| fallback | Count of `gpu transcoding unavailable` and `falling back` lines. |
| publishers | Publishers that ran to the end of the duration. |

The GPU columns are aggregated over all GPUs on the host and over the whole run, including ramp-up and teardown, so short durations dilute the average. Use the max column and the log counts to find the saturation point.

## NVENC session limits

GeForce cards cap concurrent NVENC encoder sessions in the driver. Datacenter cards (T4, A10, L4 and similar) do not. Each stream holds one encoder session per simulcast layer, so the 3-layer requests here use three per stream. Set `gpu.max_encoder_sessions` in `ingress.yaml` to match the card, or the sweep will measure the driver cap rather than the hardware.
