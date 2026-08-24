package resolver

import "path"

// buildTarget describes where to obtain a static FFmpeg build for one platform
// and how to find the binary inside the downloaded archive.
type buildTarget struct {
	// URL is the download location of the archive (zip or tar).
	URL string
	// BinaryPathInArchive is the path of the ffmpeg executable within the
	// archive. An empty string means "the first regular file", which suits
	// single-binary archives such as evermeet's.
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

// defaultBuilds is the platform build table. macOS is supported first, per the
// project's macOS-first goal. Adding Linux or Windows later is purely a matter
// of adding entries here — the resolution flow in resolver.go is platform-agnostic.
//
// evermeet.cx publishes signed, static macOS builds. The service currently
// serves x86_64 binaries, which run on Apple Silicon under Rosetta 2; when a
// native arm64 endpoint is published, only the arm64 URL below needs updating.
var defaultBuilds = map[string]buildTarget{
	"darwin/amd64": {
		URL:                 "https://evermeet.cx/ffmpeg/getrelease/ffmpeg/zip",
		BinaryPathInArchive: "", // single-binary zip
	},
	"darwin/arm64": {
		URL:                 "https://evermeet.cx/ffmpeg/getrelease/ffmpeg/zip",
		BinaryPathInArchive: "",
	},
}
