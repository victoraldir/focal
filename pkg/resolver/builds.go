package resolver

import "path"

// buildTarget describes where to obtain a static FFmpeg build for one platform
// and how to find the binary inside the downloaded archive.
type buildTarget struct {
	// URL is the download location of the archive (zip or tar). It may be a
	// redirect; the downloader follows redirects transparently.
	URL string
	// BinaryPathInArchive is the path of the ffmpeg executable within the
	// archive. An empty string means "the first regular file", which suits
	// single-binary archives such as these.
	BinaryPathInArchive string
}

// archiveName derives a stable local filename for the downloaded archive from
// the URL's extension, defaulting to .zip.
func (b buildTarget) archiveName() string {
	ext := path.Ext(b.URL)
	if ext == "" {
		ext = ".zip"
	}
	return "ffmpeg-download" + ext
}

// defaultBuilds is the platform build table. Adding a new operating system is
// purely a matter of adding entries here — the resolution flow in resolver.go is
// platform-agnostic.
//
// Every build comes from Martin Riedl's static-build service
// (https://ffmpeg.martin-riedl.de), which publishes *native* per-architecture
// binaries — a real arm64 (Apple Silicon) build rather than an x86_64 binary
// running under Rosetta 2, and a native win64 build. The
// "/redirect/latest/.../snapshot/ffmpeg.zip" URLs are stable permalinks that
// 307-redirect to the newest versioned archive, each of which contains a single
// top-level binary ("ffmpeg" on macOS, "ffmpeg.exe" on Windows).
var defaultBuilds = map[string]buildTarget{
	"darwin/arm64": {
		URL:                 "https://ffmpeg.martin-riedl.de/redirect/latest/macos/arm64/snapshot/ffmpeg.zip",
		BinaryPathInArchive: "", // single-binary zip
	},
	"darwin/amd64": {
		URL:                 "https://ffmpeg.martin-riedl.de/redirect/latest/macos/amd64/snapshot/ffmpeg.zip",
		BinaryPathInArchive: "",
	},
	// Windows: matched by base name in extractZip, so this is robust whether the
	// archive ships a single top-level "ffmpeg.exe" or nests it under a folder.
	"windows/amd64": {
		URL:                 "https://ffmpeg.martin-riedl.de/redirect/latest/windows/amd64/snapshot/ffmpeg.zip",
		BinaryPathInArchive: "ffmpeg.exe",
	},
}
