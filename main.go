// Command focal is a CLI tool that converts photo sequences into timelapses
// by wrapping FFmpeg. It resolves an FFmpeg binary (downloading a static build
// on macOS if one is not on PATH), sorts images chronologically using EXIF
// metadata, and drives FFmpeg's concat demuxer to produce the final video.
package main

import "github.com/victoraldir/focal/cmd/focal"

func main() {
	focal.Execute()
}
