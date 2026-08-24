# focal

**🎬 [Website & docs → victoraldir.github.io/focal](https://victoraldir.github.io/focal/)**

`focal` is a small, modular CLI that turns a directory of photos into a
timelapse video by wrapping [FFmpeg](https://ffmpeg.org/). It orders frames
chronologically from EXIF metadata, warns about inconsistent aspect ratios, and
— on macOS — downloads a static FFmpeg build automatically when one is not
already installed.

```
focal lapse -i ./photos -o timelapse.mp4 -f 24
```

## Features

- **Chronological ordering** — sorts by EXIF `DateTimeOriginal`, falling back to
  file modification time when EXIF is missing.
- **Aspect-ratio guard** — warns when the sequence mixes orientations, and pads
  output to even dimensions (`yuv420p`-safe) so any source resolution encodes.
- **Zero-dependency on macOS** — if `ffmpeg` isn't on your `PATH`, focal fetches
  a **native** static build (real arm64 on Apple Silicon, x86_64 on Intel) into
  `~/Library/Caches/focal/bin` and reuses it thereafter.
- **Live progress bar** — parses FFmpeg's `-progress` stream into a determinate
  `[██████░░░░] 60% · 6/10 frames` bar on a terminal, degrading to plain step
  lines when output is redirected.
- **Clean architecture** — the core use case depends only on interfaces; disk
  I/O, EXIF parsing, downloads, and process execution are all injected and
  independently unit-tested.

## Installation

### Homebrew (recommended)

```bash
brew tap victoraldir/tap
brew install --cask focal
```

### From source

```bash
go install github.com/victoraldir/focal@latest
```

### Pre-built binaries

Download a `tar.gz` for your platform from the
[releases page](https://github.com/victoraldir/focal/releases).

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

## How it works

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
   downloads and extracts a static build for the detected `darwin/arm64` or
   `darwin/amd64` target.
3. **Generate concat file** — `pkg/ffmpeg` writes an FFmpeg concat-demuxer file
   (`file '...'` + `duration`) to a temp path, cleaned up on exit.
4. **Encode** — FFmpeg runs with
   `-f concat -safe 0 -i <file> -vf "pad=ceil(iw/2)*2:ceil(ih/2)*2" -pix_fmt yuv420p`,
   streaming progress to stderr.

## Development

```bash
go build ./...          # compile
go test ./...           # run the full unit-test suite
go test -race -cover ./pkg/...
go run . lapse -i ./photos
```

### Project layout

```
cmd/focal      Cobra commands (composition root: root.go, lapse.go)
pkg/domain     Interfaces, models, and the Timelapser use case
pkg/scanner    Image header + EXIF extraction (domain.ImageScanner)
pkg/ffmpeg     Concat generator + command executor (domain.Encoder)
pkg/resolver   macOS binary downloader + PATH checker (domain.BinaryResolver)
```

### Adding another platform

The resolver is platform-agnostic: to support Linux or Windows, add an entry to
the `defaultBuilds` table in `pkg/resolver/builds.go` (URL + in-archive binary
path) and, for non-zip archives, a branch in `extractArchive`. No changes to the
domain or resolution flow are required.

## Releasing (maintainers)

Releases are automated with [GoReleaser](https://goreleaser.com) and GitHub
Actions. See [`RELEASING.md`](./RELEASING.md) for the one-time tap setup and the
tag-to-publish flow.

## License

[MIT](./LICENSE)
