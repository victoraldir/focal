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
// macOS builds come from Martin Riedl's static-build service
// (https://ffmpeg.martin-riedl.de), which publishes native per-architecture
// binaries. The Windows build comes from Gyan.dev's maintained Windows builds;
// its stable essentials URL contains ffmpeg.exe below a versioned directory.
var defaultBuilds = map[string]buildTarget{
	"darwin/arm64": {
		URL:                 "https://ffmpeg.martin-riedl.de/redirect/latest/macos/arm64/snapshot/ffmpeg.zip",
		BinaryPathInArchive: "", // single-binary zip
	},
	"darwin/amd64": {
		URL:                 "https://ffmpeg.martin-riedl.de/redirect/latest/macos/amd64/snapshot/ffmpeg.zip",
		BinaryPathInArchive: "",
	},
	// Gyan.dev publishes a stable URL for the latest win64 essentials build. The
	// extractor matches by base name, so the versioned top-level directory does
	// not need to be encoded here.
	"windows/amd64": {
		URL:                 "https://www.gyan.dev/ffmpeg/builds/ffmpeg-release-essentials.zip",
		BinaryPathInArchive: "ffmpeg.exe",
	},
}
