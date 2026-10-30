# focal

**🎬 [Website & docs → victoraldir.github.io/focal](https://victoraldir.github.io/focal/)**

`focal` is a small, modular CLI that turns a directory of photos into a
timelapse video by wrapping [FFmpeg](https://ffmpeg.org/). It orders frames
chronologically from EXIF metadata, warns about inconsistent aspect ratios, and
— on macOS and Windows — downloads a static FFmpeg build automatically when one
is not already installed.

With a Wi-Fi capable Sony camera it also covers the capture side: `focal shoot`
runs an interval timelapse straight from the terminal, and `focal serve` opens a
browser studio with live view, remote control and interactive API docs.

| Command        | What it does |
| -------------- | ------------ |
| `focal lapse`  | Photos in a folder → timelapse video |
| `focal shoot`  | Interval capture from a Sony camera over Wi-Fi, optionally rendered straight to video |
| `focal serve`  | Browser studio: live view, shutter, settings, timelapse capture & rendering, Swagger API docs |

## Quick start

```bash
# Already have the photos?
focal lapse -i ./photos -o timelapse.mp4 -f 24

# Let focal drive the camera: one frame every 5 s for an hour, then render
focal shoot -t 5s -d 1h --lapse sunset.mp4

# Point-and-click: live view and remote control in the browser
focal serve

# No camera at hand? Every camera command accepts --simulate
focal serve --simulate
```

## Features

- **Chronological ordering** — sorts by EXIF `DateTimeOriginal`, falling back to
  file modification time when EXIF is missing.
- **Aspect-ratio guard** — warns when the sequence mixes orientations, and pads
  output to even dimensions (`yuv420p`-safe) so any source resolution encodes.
- **Zero-dependency on macOS & Windows** — if `ffmpeg` isn't on your `PATH`,
  focal fetches a **native** static build and reuses it thereafter. On macOS it
  lands in `~/Library/Caches/focal/bin` (real arm64 on Apple Silicon, x86_64 on
  Intel); on Windows it's `ffmpeg.exe` in `%LOCALAPPDATA%\focal\bin`.
- **Live progress bar** — parses FFmpeg's `-progress` stream into a determinate
  `[██████░░░░] 60% · 6/10 frames` bar on a terminal, degrading to plain step
  lines when output is redirected.
- **Interval capture from the camera** — `focal shoot` fires a Sony camera's
  shutter over Wi-Fi on a drift-free schedule, survives the odd dropped shot,
  and can render the video the moment capture ends.
- **Browser studio** — `focal serve` gives you live view, tap-to-focus, power
  zoom, exposure controls, timelapse progress and one-click rendering, plus
  Swagger UI docs for its REST API. Everything is embedded in the binary, so it
  works on the camera's internet-less Wi-Fi.
- **Camera simulator** — `--simulate` swaps in a built-in fake camera (an
  animated sunset) to try the workflow, develop, and test without hardware.
- **Clean architecture** — the core use cases depend only on interfaces; disk
  I/O, EXIF parsing, downloads, and process execution are all injected and
  independently unit-tested.

## Installation

### macOS — Homebrew (recommended)

```bash
brew tap victoraldir/tap
brew install --cask focal
```

### Windows — Scoop (recommended)

[Scoop](https://scoop.sh) is a free, open-source package manager that needs no
administrator rights:

```powershell
scoop bucket add focal https://github.com/victoraldir/scoop-bucket
scoop install focal
```

Upgrade later with `scoop update focal`.

### From source

```bash
go install github.com/victoraldir/focal@latest
```

### Pre-built binaries

Download an archive for your platform from the
[releases page](https://github.com/victoraldir/focal/releases) — `tar.gz` for
macOS, `.zip` for Windows.

## Usage

```bash
focal lapse --input <dir> [--output <file>] [--fps <n>]
```

| Flag              | Short | Default          | Description                                   |
| ----------------- | ----- | ---------------- | --------------------------------------------- |
| `--input`         | `-i`  | *(required)*     | Directory containing source photos            |
| `--output`        | `-o`  | `timelapse.mp4`  | Output video file path                        |
| `--fps`           | `-f`  | `30`             | Output frames per second                      |
| `--max-height`    |       | `1080`           | Cap output height (px) for smooth playback; `0` keeps source resolution |

> **Why `--max-height`?** Full-sensor stills (e.g. a 24 MP mirrorless at
> 6000×3376) would otherwise produce ~6K video that exceeds every hardware H.264
> decoder's limit — it falls back to software decoding and stutters. focal caps
> output at 1080p by default so it plays smoothly everywhere and stays easy to
> share. Raise it (`--max-height 2160` for 4K) or disable it (`--max-height 0`
> to keep full resolution) as you like.

Example — build a 24 fps timelapse and overwrite the default output name:

```bash
focal lapse -i ~/Pictures/sunset -o sunset.mp4 -f 24
```

Keep the full sensor resolution instead of the 1080p cap:

```bash
focal lapse -i ~/Pictures/sunset --max-height 0
```

Check the build:

```bash
focal --version        # terse: focal version x.y.z
focal version          # detailed: version, commit, build date, Go, platform
```

## Shooting with a Sony camera

`focal shoot` and `focal serve` drive Sony bodies over Wi-Fi through the
camera's built-in **Smart Remote** app (Camera Remote API) — e.g. a5000, a6000,
NEX-5T/6, RX100 III/IV and the QX lens cameras.

**Supported cameras.** Any body that runs Sony's built-in *Smart Remote* app
and speaks the Camera Remote API should work. That includes the a5000 (ILCE-5000),
a5100, a6000, a7/a7R/a7S (first generation), NEX-5T/NEX-6, RX100 III/IV,
HX60/HX90, and the QX10/QX100 lens cameras. Newer bodies that dropped the
Camera Remote API (a6400 and later, which use *Imaging Edge*) are not supported.
Development used an a5000-faithful simulator; reports from other bodies are welcome.

**Connect the camera**

1. On the camera: **Menu → Application List → Smart Remote Embedded**.
2. Join the Wi-Fi network it displays (`DIRECT-xxxx:ILCE-5000`) from your computer.
3. Run a command — focal finds the camera via SSDP (falling back to the
   well-known addresses `192.168.122.1:8080` and `10.0.0.1:10000`). Pass
   `--camera http://…/sony` to skip discovery.

> **Can the camera join my home Wi-Fi instead?** Not for remote control. The
> a5000 joins a home network only for *Send to Computer* transfers; Smart Remote
> always runs as the camera's own access point. To keep internet access while
> shooting, use Ethernet or a second (USB) Wi-Fi adapter. focal needs no
> internet: the studio and its API docs are fully embedded.

### `focal shoot` — interval capture

```bash
focal shoot -t 5s -n 300 -o ./sunset            # 300 frames, one every 5 s
focal shoot -t 10s -d 1h --lapse sunset.mp4     # shoot for an hour, then render
focal shoot --simulate -t 1s -n 20              # try it without a camera
```

| Flag             | Short | Default                  | Description |
| ---------------- | ----- | ------------------------ | ----------- |
| `--interval`     | `-t`  | `5s`                     | Time between shots |
| `--count`        | `-n`  | `0`                      | Frames to capture (`0` = until `--duration` or Ctrl-C) |
| `--duration`     | `-d`  | `0`                      | Session length (`0` = until `--count` or Ctrl-C) |
| `--output`       | `-o`  | `shoot-YYYYMMDD-HHMMSS`  | Directory for frames |
| `--size`         |       | `original`               | Transfer size: `original` (full res), `2m` (~2 MP, faster), `keep` |
| `--lapse`        |       |                          | Render a video to this path when capture ends |
| `--fps` / `--max-height` | `-f` | `30` / `1080`     | Passed to the render, as in `focal lapse` |
| `--camera`       |       | auto-discover            | Camera API URL |
| `--simulate`     |       | `false`                  | Use the built-in camera simulator |

Shots are scheduled against the session start, so timing never drifts; a single
failed shot (Wi-Fi blip) is logged and skipped, three in a row abort. Ctrl-C
stops capture and still renders what was captured when `--lapse` is set.
Full-resolution transfers take a few seconds over Wi-Fi — for intervals under
~4 s use `--size 2m`.

### `focal serve` — browser studio

```bash
focal serve                      # opens http://localhost:8770
focal serve --simulate           # explore the studio without a camera
```

**Studio (`/`)**

- **Viewfinder** — MJPEG live view with an exposure HUD (mode, shutter,
  aperture, ISO, EV, focus state), a rule-of-thirds grid, fullscreen, and
  click-to-focus on bodies that support touch AF.
- **Shooting** — shutter button (or <kbd>Space</kbd>), press-and-hold power
  zoom (<kbd>+</kbd>/<kbd>−</kbd>) and press-and-hold AF (half-press).
- **Exposure & image** — mode, shutter speed, aperture, ISO, exposure
  compensation, white balance, focus mode, transfer size, self-timer and flash.
  Each control offers only the values the camera accepts right now and greys
  out when the current mode doesn't allow it.
- **Timelapse** — a planner that shows frames, shooting time and clip length
  as you type; live progress with countdown, latest frame and warnings; then
  one-click rendering to MP4 (24/25/30/60 fps) through the same pipeline as
  `focal lapse`.
- **Library** — every photo, timelapse session and video in `--media`
  (default `./focal-media`), with a lightbox viewer and downloads.
- **Status bar** — connection, battery level and remaining shots on the card.

**API docs (`/docs`)** — Swagger UI with "Try it out" enabled, served from the
embedded OpenAPI spec at `/openapi.yaml`.

| Endpoint | Purpose |
| -------- | ------- |
| `GET /api/camera` · `POST /api/camera/connect` | Connection info · (re)discover the camera |
| `GET /api/status` | Settings with their allowed values, battery, card space, zoom, focus |
| `PUT /api/settings/{name}` | Change a setting, e.g. `{"value": "1/250"}` |
| `POST /api/capture` | Shoot, download and store a photo |
| `GET /api/liveview` · `GET /api/liveview/snapshot` | MJPEG stream · single JPEG frame |
| `POST /api/zoom` · `POST /api/focus/{half-press,release,touch}` | Zoom and focus |
| `GET/POST/DELETE /api/timelapse` · `POST /api/timelapse/render` | Interval sessions and rendering |
| `GET /api/media` · `GET /media/{path}` | Library listing and files |
| `GET /api/rpc/methods` · `POST /api/rpc/{service}` | Raw Camera Remote API passthrough |

```bash
# Script the camera from anywhere
curl -X PUT localhost:8770/api/settings/exposureMode -d '{"value":"Manual"}'
curl -X PUT localhost:8770/api/settings/shutterSpeed -d '{"value":"1/250"}'
curl -X POST localhost:8770/api/capture
curl -X POST localhost:8770/api/rpc/camera -d '{"method":"getAvailableApiList","params":[]}'
```

The server binds to `127.0.0.1` by default; use `--addr 0.0.0.0:8770` to open
the studio from a phone on the same network.

### Troubleshooting

| Symptom | Fix |
| ------- | --- |
| `no camera found (SSDP: no SSDP response)` | The computer isn't on the camera's network. Open Smart Remote on the camera and join its `DIRECT-…` Wi-Fi; your IP should become `192.168.122.x`. VPNs and firewalls can also block SSDP. Pass `--camera http://192.168.122.1:8080/sony` to skip discovery. |
| Every studio control is greyed out | No camera is connected (see above). The connection pill in the top-right shows the state; click **Connect** after joining the camera's Wi-Fi. |
| Shutter or aperture greyed out | The exposure mode doesn't allow it: shutter speed needs **S**/**M**, aperture needs **A**/**M**, and ISO/WB/EV are locked in Intelligent Auto. Change **Mode** first. |
| `camera does not offer "Original" transfer size` | The body only transfers ~2 MP previews over Wi-Fi. Frames are still fine for 1080p video. |
| `capture is slower than the interval` | Full-resolution transfers take a few seconds over Wi-Fi. Lengthen `--interval` or use `--size 2m`. |
| Camera stops responding mid-session | Disable auto power-off on the camera and use a charged battery or USB power; focal gives up after three consecutive failed shots and keeps every frame already captured. |

## How it works

### `focal lapse`

```
cmd/focal ──constructs──▶ domain.Timelapser (use case)
                              │ depends only on interfaces
        ┌─────────────┬───────┴────────┬──────────────┐
   ImageScanner   BinaryResolver   ConcatBuilder    Encoder
   (scanner)      (resolver)       (ffmpeg)         (ffmpeg)
        │              │                                │
   EXIF + headers  PATH/cache/download            FFmpegRunner (os/exec)
```

1. **Scan** — `pkg/scanner` reads image headers for dimensions and resolves each
   frame's timestamp (EXIF, then mtime), returning the sequence sorted oldest-first.
2. **Resolve FFmpeg** — `pkg/resolver` checks `PATH`, then the user cache, then
   downloads and extracts a static build for the detected target
   (`darwin/arm64`, `darwin/amd64`, or `windows/amd64`).
3. **Generate concat file** — `pkg/ffmpeg` writes an FFmpeg concat-demuxer file
   (`file '...'` + `duration`) to a temp path, cleaned up on exit.
4. **Encode** — FFmpeg runs with
   `-f concat -safe 0 -i <file> -vf "pad=ceil(iw/2)*2:ceil(ih/2)*2" -pix_fmt yuv420p`,
   streaming progress to stderr.

### `focal shoot` and `focal serve`

```
cmd/focal ──constructs──▶ domain.Shooter (use case) ──▶ domain.Timelapser (--lapse / Render)
                              │ depends only on interfaces
                    ┌─────────┴──────────┐
                 Camera              FrameStore
              (camera/sony)         (framestore)
                    │
   SSDP discovery · JSON-RPC client · live-view decoder
                    │
             Sony Smart Remote (camera Wi-Fi)

pkg/server ── REST API + MJPEG fan-out + embedded UI/Swagger ──▶ camera/sony, domain.Shooter
```

1. **Discover** — an SSDP `M-SEARCH` for `urn:schemas-sony-com:service:ScalarWebAPI:1`
   finds the camera's device description and its API endpoint; known default
   addresses are probed if multicast is blocked.
2. **Prepare** — focal enters remote shooting mode (`startRecMode`), selects
   still mode and the requested transfer size, and waits for the body to go idle.
3. **Capture** — `actTakePicture` (following `awaitTakePicture` for long
   exposures), then the image is downloaded and written atomically with its
   capture time as mtime, so `focal lapse` orders frames correctly even without EXIF.
4. **Live view** — the camera's binary packet stream is decoded into JPEGs and
   fanned out to every browser viewer as MJPEG over a single camera connection.

## Development

```bash
go build ./...          # compile
go test ./...           # run the full unit-test suite
go test -race -cover ./pkg/...
go run . lapse -i ./photos
go run . shoot --simulate -t 1s -n 10   # interval capture against the simulator
go run . serve --simulate               # studio against the simulator
```

The simulator (`pkg/camera/sony/sonysim`) models rec-mode gating,
exposure-mode-dependent settings, zoom, focus, postview downloads and the
binary live-view stream, and backs the camera and server test suites.

### Project layout

```
cmd/focal      Cobra commands (composition root: root.go, lapse.go, shoot.go, serve.go)
pkg/domain     Interfaces, models, and the Timelapser and Shooter use cases
pkg/scanner    Image header + EXIF extraction (domain.ImageScanner)
pkg/ffmpeg     Concat generator + command executor (domain.Encoder)
pkg/resolver   macOS/Windows binary downloader + PATH checker (domain.BinaryResolver)
pkg/camera/sony         Camera Remote API client, SSDP discovery, live view (domain.Camera)
pkg/camera/sony/sonysim In-process camera simulator used by tests and --simulate
pkg/framestore          Atomic frame writer (domain.FrameStore)
pkg/server              HTTP studio, REST API, OpenAPI spec and embedded Swagger UI
```

### Adding another platform

The resolver is platform-agnostic: macOS (arm64/amd64) and Windows (amd64) ship
today. To support Linux, add an entry to the `defaultBuilds` table in
`pkg/resolver/builds.go` (URL + in-archive binary path) and, for non-zip
archives, a branch in `extractArchive`. No changes to the domain or resolution
flow are required — `Resolver.binaryName` already appends `.exe` on Windows and
`isExecutable` already accounts for platforms without a Unix execute bit.

## Releasing (maintainers)

Releases are automated with [GoReleaser](https://goreleaser.com) and GitHub
Actions. See [`RELEASING.md`](./RELEASING.md) for the one-time tap setup and the
tag-to-publish flow.

## License

[MIT](./LICENSE)
