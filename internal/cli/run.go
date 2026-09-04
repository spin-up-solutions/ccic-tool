package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-up-solutions/ccic-tool/internal/dock"
	"github.com/spin-up-solutions/ccic-tool/internal/render"
)

func startCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start [-- claude args...]",
		Short: "Start the containers and run Claude",
		Long: "Fails if init or build has not been run — ccic never builds\n" +
			"implicitly. Arguments after -- are passed through to claude,\n" +
			"so `ccic start -- --resume` works.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dock.Available(); err != nil {
				return err
			}
			p, err := load()
			if err != nil {
				return err
			}
			if err := p.requireBuilt(); err != nil {
				return err
			}
			if err := p.up(); err != nil {
				return err
			}

			claudeArgs := []string{"claude"}
			if p.cfg.Claude.SkipPermissions {
				claudeArgs = append(claudeArgs, "--dangerously-skip-permissions")
			}
			claudeArgs = append(claudeArgs, p.cfg.Claude.ExtraArgs...)
			claudeArgs = append(claudeArgs, args...)

			info("workspace %s", p.cfg.Workspace)
			return p.compose.Exec(render.User, svcClaude, true, claudeArgs...)
		},
	}
	return cmd
}

func shellCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "shell",
		Short: "Open an interactive shell in the container",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if err := p.requireBuilt(); err != nil {
				return err
			}
			if err := p.up(); err != nil {
				return err
			}
			return p.compose.Exec(render.User, svcClaude, true, "zsh", "-l")
		},
	}
}

func execCmd() *cobra.Command {
	return &cobra.Command{
		Use:                   "exec <command>...",
		Short:                 "Run a one-off command in the container",
		Args:                  cobra.MinimumNArgs(1),
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if err := p.requireBuilt(); err != nil {
				return err
			}
			if err := p.up(); err != nil {
				return err
			}
			// Through a login shell so pipes, globs and mise activation behave
			// the way they would if the user typed the command themselves.
			return p.compose.Exec(render.User, svcClaude, true, "zsh", "-lc", strings.Join(args, " "))
		},
	}
}

func psqlCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "psql [-- psql args...]",
		Short: "Open a psql session against the project database",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if !p.cfg.Postgres.Enabled {
				return fmt.Errorf("postgres is not enabled for %s", p.cfg.Suffix)
			}
			if err := p.requireBuilt(); err != nil {
				return err
			}
			if err := p.up(); err != nil {
				return err
			}
			return p.compose.Exec(render.User, svcClaude, true, append([]string{"psql"}, args...)...)
		},
	}
}

func upCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "up",
		Short: "Start the containers without running Claude",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if err := p.requireBuilt(); err != nil {
				return err
			}
			if err := p.up(); err != nil {
				return err
			}
			okay("containers up")
			return nil
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the containers, keeping images and volumes",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if err := p.compose.Run("stop"); err != nil {
				return err
			}
			okay("stopped")
			return nil
		},
	}
}

func logsCmd() *cobra.Command {
	var follow bool
	cmd := &cobra.Command{
		Use:   "logs [service]",
		Short: "Show container logs",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			a := []string{"logs"}
			if follow {
				a = append(a, "-f")
			}
			return p.compose.Run(append(a, args...)...)
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "follow log output")
	return cmd
}
