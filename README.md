# focal

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
  a static build into `~/Library/Caches/focal/bin` and reuses it thereafter.
- **Clean architecture** — the core use case depends only on interfaces; disk
  I/O, EXIF parsing, downloads, and process execution are all injected and
  independently unit-tested.

## Installation

### Homebrew (recommended)

```bash
brew tap victoraldir/tap
brew install focal
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

| Flag              | Short | Default          | Description                         |
| ----------------- | ----- | ---------------- | ----------------------------------- |
| `--input`         | `-i`  | *(required)*     | Directory containing source photos  |
| `--output`        | `-o`  | `timelapse.mp4`  | Output video file path              |
| `--fps`           | `-f`  | `30`             | Output frames per second            |

Example — build a 24 fps timelapse and overwrite the default output name:

```bash
focal lapse -i ~/Pictures/sunset -o sunset.mp4 -f 24
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
