// Package focal wires the CLI. It constructs the concrete adapters (scanner,
// resolver, ffmpeg engine, logger), injects them into the domain use case, and
// exposes them through cobra commands. This is the composition root: it is the
// only layer permitted to know about every other package.
package focal

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// version is overridden at build time via -ldflags by GoReleaser.
var version = "dev"

// rootCmd is the base `focal` command. It does no work itself; functionality
// lives in subcommands such as `focal lapse`.
var rootCmd = &cobra.Command{
	Use:     "focal",
	Short:   "focal converts photo sequences into timelapses",
	Version: version,
	Long: `focal is a CLI tool that converts a directory of photos into a timelapse
video by wrapping FFmpeg.

It sorts images chronologically using EXIF metadata (falling back to file
modification time), warns about inconsistent aspect ratios, and downloads a
static FFmpeg build automatically on macOS when one is not already installed.`,
}

// Execute runs the root command and exits non-zero on failure. It is the single
// entry point called from main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(newLapseCmd())
	rootCmd.AddCommand(newVersionCmd())
}
