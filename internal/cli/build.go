package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-up-solutions/ccic-tool/internal/dock"
	"github.com/spin-up-solutions/ccic-tool/internal/mise"
	"github.com/spin-up-solutions/ccic-tool/internal/render"
)

func buildCmd() *cobra.Command {
	var noCache, rebuildBase bool
	cmd := &cobra.Command{
		Use:   "build",
		Short: "Build or rebuild this project's container",
		Long: "Builds the shared base image if it is missing, then the thin\n" +
			"per-project layer that installs the toolchain from mise.toml.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dock.Available(); err != nil {
				return err
			}
			p, err := load()
			if err != nil {
				return err
			}
			if err := ensureBase(p, rebuildBase); err != nil {
				return err
			}
			if err := regen(p); err != nil {
				return err
			}
			_, n, err := mise.ExtractTools(p.dir)
			if err != nil {
				return err
			}
			if n == 0 {
				info("no mise.toml [tools] — project layer installs no extra toolchains")
			} else {
				info("installing %d toolchain(s) from mise.toml", n)
			}
			// Catch a malformed generated file here, where the message can name
			// ccic, rather than deep inside a docker build.
			if out, err := p.compose.Capture("config", "-q"); err != nil {
				return fmt.Errorf("generated compose.yml is invalid: %s", firstLine(out))
			}
			args2 := []string{"build"}
			if noCache {
				args2 = append(args2, "--no-cache")
			}
			if err := p.compose.Run(append(args2, svcClaude)...); err != nil {
				return err
			}
			okay("built %s", p.view.ImageName)
			return nil
		},
	}
	cmd.Flags().BoolVar(&noCache, "no-cache", false, "build the project layer without cache")
	cmd.Flags().BoolVar(&rebuildBase, "base", false, "rebuild the shared base image too")
	return cmd
}

// ensureBase builds the shared base image if needed.
//
// One base per (ccic version, host uid, host gid) is shared by every project on
// the machine, which is the difference between one multi-gigabyte image and one
// per project.
func ensureBase(p *project, force bool) error {
	ref := p.view.BaseImage
	if dock.ImageExists(ref) && !force {
		return nil
	}
	if force {
		info("rebuilding base image %s", ref)
	} else {
		info("base image %s is missing — building it now.", ref)
		info("this takes several minutes, but only once per machine.")
	}

	tmp, err := os.MkdirTemp("", "ccic-base-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	df, err := render.BaseDockerfile(tmp)
	if err != nil {
		return err
	}
	buildArgs := map[string]string{
		"UID":                fmt.Sprint(p.view.UID),
		"GID":                fmt.Sprint(p.view.GID),
		"USERNAME":           render.User,
		"BASE_IMAGE":         p.cfg.Image.Base,
		"NODE_MAJOR":         p.cfg.Image.NodeMajor,
		"PLAYWRIGHT_VERSION": p.cfg.Image.PlaywrightVersion,
	}
	if err := dock.Build(df, tmp, ref, buildArgs, force); err != nil {
		return fmt.Errorf("building base image: %w", err)
	}
	okay("built %s", ref)
	return nil
}

func regen(p *project) error {
	if err := p.view.Context(); err != nil {
		return err
	}
	if err := p.view.Doc(); err != nil {
		return err
	}
	return nil
}

func regenCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "regen",
		Short: "Regenerate the build context and .ccic.md without rebuilding",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if err := regen(p); err != nil {
				return err
			}
			okay("regenerated %s/ and %s", render.DirName, render.DocName)
			return nil
		},
	}
}

func forceRebuildCmd() *cobra.Command {
	var withVolumes, withBase bool
	cmd := &cobra.Command{
		Use:   "force-rebuild",
		Short: "Tear down and rebuild this project's image with --no-cache",
		Long: "Rebuilds the image from scratch. Volumes are preserved by default:\n" +
			"force-rebuild is about the image, not the data, and wiping volumes\n" +
			"would delete your Claude login and your database every time.",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dock.Available(); err != nil {
				return err
			}
			p, err := load()
			if err != nil {
				return err
			}
			down := []string{"down", "--rmi", "local", "--remove-orphans"}
			if withVolumes {
				if err := confirmDestructive(
					fmt.Sprintf("This deletes the database and the Claude login for %s.", p.cfg.Suffix)); err != nil {
					return err
				}
				down = append(down, "-v")
			}
			_ = p.compose.Run(down...)
			if err := ensureBase(p, withBase); err != nil {
				return err
			}
			if err := regen(p); err != nil {
				return err
			}
			if err := p.compose.Run("build", "--no-cache", svcClaude); err != nil {
				return err
			}
			if err := p.up(); err != nil {
				return err
			}
			if withVolumes {
				okay("rebuilt from scratch (volumes wiped)")
			} else {
				okay("rebuilt from scratch (login and database preserved)")
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&withVolumes, "volumes", false, "also wipe volumes (deletes the database and Claude login)")
	cmd.Flags().BoolVar(&withBase, "base", false, "rebuild the shared base image too")
	return cmd
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
