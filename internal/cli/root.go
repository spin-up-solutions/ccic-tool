// Package cli implements the ccic commands.
package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/spin-up-solutions/ccic-tool/internal/config"
	"github.com/spin-up-solutions/ccic-tool/internal/dock"
	"github.com/spin-up-solutions/ccic-tool/internal/render"
)

// Version is set at build time via -ldflags.
var Version = "dev"

const (
	svcClaude = "claude"
	svcDB     = "db"
)

var projectDir string

func Execute() error {
	root := &cobra.Command{
		Use:   "ccic",
		Short: "Run Claude Code in a container, isolated from your dev environment",
		Long: "ccic runs Claude Code in a Docker container with its own toolchain,\n" +
			"database and browser, deliberately kept apart from the copy of the\n" +
			"project you run on your own machine.",
		SilenceUsage:  true,
		SilenceErrors: true,
		Version:       Version,
	}
	root.PersistentFlags().StringVarP(&projectDir, "dir", "C", "", "project directory (default: current)")

	root.AddCommand(
		initCmd(), buildCmd(), startCmd(), shellCmd(), execCmd(), psqlCmd(),
		upCmd(), stopCmd(), logsCmd(), statusCmd(), doctorCmd(), firewallCmd(),
		regenCmd(), destroyCmd(), forceRebuildCmd(), pruneCmd(),
	)
	return root.Execute()
}

func dir() (string, error) {
	d := projectDir
	if d == "" {
		var err error
		if d, err = os.Getwd(); err != nil {
			return "", err
		}
	}
	return filepath.Abs(d)
}

// project bundles everything a command needs about the current project.
type project struct {
	dir     string
	cfg     *config.Config
	view    *render.View
	compose dock.Compose
}

func load() (*project, error) {
	d, err := dir()
	if err != nil {
		return nil, err
	}
	cfg, err := config.Load(d)
	if err != nil {
		return nil, err
	}
	v := render.NewView(cfg, d, Version, os.Getuid(), os.Getgid())
	return &project{
		dir:  d,
		cfg:  cfg,
		view: v,
		compose: dock.Compose{
			ProjectName: render.ProjectName(cfg),
			Dir:         filepath.Join(d, render.DirName),
		},
	}, nil
}

// requireBuilt refuses to act when init or build has not been run. ccic never
// builds implicitly: controlling when a rebuild happens is the point.
func (p *project) requireBuilt() error {
	if _, err := os.Stat(filepath.Join(p.dir, render.DirName, "compose.yml")); err != nil {
		return fmt.Errorf("no build context — run `ccic build` first")
	}
	if !dock.ImageExists(p.view.ImageName) {
		return fmt.Errorf("image %s does not exist — run `ccic build` first", p.view.ImageName)
	}
	return nil
}

// up starts the stack and waits for the entrypoint to finish, so anything that
// inspects the container afterwards sees settled state.
func (p *project) up() error {
	if p.compose.Running(svcClaude) {
		return p.compose.RunQuiet("up", "-d", "--wait")
	}
	return p.compose.Run("up", "-d", "--wait")
}

func info(format string, a ...any) { fmt.Fprintf(os.Stderr, "\033[36mccic:\033[0m "+format+"\n", a...) }
func okay(format string, a ...any) { fmt.Fprintf(os.Stderr, "\033[32mccic:\033[0m "+format+"\n", a...) }
func warn(format string, a ...any) { fmt.Fprintf(os.Stderr, "\033[33mccic:\033[0m "+format+"\n", a...) }
