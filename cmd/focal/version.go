package focal

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Build metadata, overridden at release time via -ldflags -X (see
// .goreleaser.yaml). They default to development-friendly placeholders so a
// `go build` / `go run` still reports something sensible.
var (
	commit = "none"
	date   = "unknown"
)

// newVersionCmd builds `focal version`, which prints full build details. This
// complements the terse `focal --version` flag that cobra wires from the root
// command's Version field.
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print detailed version and build information",
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, _ []string) {
			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "focal %s\n", version)
			fmt.Fprintf(w, "  commit:   %s\n", commit)
			fmt.Fprintf(w, "  built:    %s\n", date)
			fmt.Fprintf(w, "  go:       %s\n", runtime.Version())
			fmt.Fprintf(w, "  platform: %s/%s\n", runtime.GOOS, runtime.GOARCH)
		},
	}
}
