package cli

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
)

// Set at build time via -ldflags; see .goreleaser.yaml.
var (
	Commit    = "none"
	BuildDate = "unknown"
)

// Repo is the source of both releases and self-updates.
const Repo = "spin-up-solutions/ccic-tool"

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print version, commit and platform",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("ccic %s\n", Version)
			fmt.Printf("  commit   %s\n", Commit)
			fmt.Printf("  built    %s\n", BuildDate)
			fmt.Printf("  platform %s/%s\n", runtime.GOOS, runtime.GOARCH)
			fmt.Printf("  go       %s\n", runtime.Version())
			return nil
		},
	}
}
