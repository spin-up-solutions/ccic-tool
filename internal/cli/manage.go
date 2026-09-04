package cli

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/spin-up-solutions/ccic-tool/internal/config"
	"github.com/spin-up-solutions/ccic-tool/internal/dock"
	"github.com/spin-up-solutions/ccic-tool/internal/render"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show what is configured, built and running",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			row := func(k, v string) { fmt.Printf("  %-14s %s\n", k, v) }
			fmt.Println()
			row("suffix", p.cfg.Suffix)
			row("workspace", fmt.Sprintf("%s  ←  %s", p.cfg.Workspace, p.dir))
			row("screenshots", filepath.Join(p.dir, p.cfg.Isolation.Screenshots))
			row("base image", imageLine(p.view.BaseImage))
			row("image", imageLine(p.view.ImageName))
			row("isolated", strings.Join(p.cfg.Isolation.Paths, " "))
			if p.cfg.Postgres.Enabled {
				row("postgres", "v"+p.cfg.Postgres.Version+"  db="+p.cfg.Postgres.Database)
			} else {
				row("postgres", "disabled")
			}
			if p.cfg.Redis.Enabled {
				row("redis", "v"+p.cfg.Redis.Version)
			}
			if len(p.cfg.Network.Publish) == 0 {
				row("ports", "none published (host runs its own copy)")
			} else {
				row("ports", fmt.Sprint(p.cfg.Network.Publish))
			}
			fmt.Println()
			_ = p.compose.Run("ps")

			if p.compose.Running(svcClaude) {
				fmt.Println()
				fw, _ := p.compose.ExecCapture("root", svcClaude, "/usr/local/bin/ccic-firewall", "status")
				row("firewall", fw)
				tools, _ := p.compose.ExecCapture(render.User, svcClaude, "mise", "ls", "--installed")
				row("tools", strings.Join(strings.Fields(tools), " "))
				auth, _ := p.compose.ExecCapture(render.User, svcClaude, "sh", "-c",
					`test -f "$CLAUDE_CONFIG_DIR/.credentials.json" && echo "logged in" || echo "not logged in"`)
				row("claude auth", auth)
			}
			return nil
		},
	}
}

func imageLine(ref string) string {
	if !dock.ImageExists(ref) {
		return ref + "  (not built)"
	}
	created := dock.ImageCreated(ref)
	if len(created) > 19 {
		created = created[:19]
	}
	return ref + "  built " + created
}

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check that everything this project needs is in place",
		RunE: func(cmd *cobra.Command, args []string) error {
			var failed int
			check := func(name string, err error, hint string) {
				if err == nil {
					fmt.Printf("  \033[32m✓\033[0m %s\n", name)
					return
				}
				failed++
				fmt.Printf("  \033[31m✗\033[0m %s — %v\n", name, err)
				if hint != "" {
					fmt.Printf("      %s\n", hint)
				}
			}
			fmt.Println()
			check("docker is running", dock.Available(), "start Docker Desktop")

			d, err := dir()
			if err != nil {
				return err
			}
			_, cfgErr := config.Load(d)
			check(config.FileName+" parses", cfgErr, "run `ccic init`")
			if cfgErr != nil {
				fmt.Println()
				return fmt.Errorf("%d check(s) failed", failed)
			}

			p, err := load()
			if err != nil {
				return err
			}
			check("base image built", existsErr(p.view.BaseImage), "run `ccic build`")
			check("project image built", existsErr(p.view.ImageName), "run `ccic build`")

			ctxErr := error(nil)
			if _, err := os.Stat(filepath.Join(p.dir, render.DirName, "compose.yml")); err != nil {
				ctxErr = fmt.Errorf("missing")
			}
			check("build context present", ctxErr, "run `ccic build`")

			docErr := error(nil)
			if _, err := os.Stat(filepath.Join(p.dir, render.DocName)); err != nil {
				docErr = fmt.Errorf("missing")
			}
			check(render.DocName+" present", docErr, "run `ccic regen`")

			if p.compose.Running(svcClaude) {
				check("container running", nil, "")
				if p.cfg.Postgres.Enabled {
					_, err := p.compose.ExecCapture(render.User, svcClaude, "pg_isready", "-h", "db", "-q")
					check("database reachable", err, "run `ccic logs db`")
				}
				// Not being logged in yet is the normal state of a fresh project,
				// so report it without failing the run.
				out, _ := p.compose.ExecCapture(render.User, svcClaude, "sh", "-c",
					`test -f "$CLAUDE_CONFIG_DIR/.credentials.json" && echo yes`)
				if strings.Contains(out, "yes") {
					check("claude authenticated", nil, "")
				} else {
					note("claude not signed in yet — `ccic start` prompts once, then it persists")
				}
			} else {
				note("container not running — `ccic up` for the rest of the checks")
			}

			fmt.Println()
			if failed > 0 {
				return fmt.Errorf("%d check(s) failed", failed)
			}
			okay("all checks passed")
			return nil
		},
	}
}

func existsErr(ref string) error {
	if dock.ImageExists(ref) {
		return nil
	}
	return fmt.Errorf("missing")
}

func firewallCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "firewall <on|off|status|allow <domain>...>",
		Short: "Inspect or change the egress firewall in the running container",
		Long: "Changes apply immediately, with no rebuild or restart.\n\n" +
			"`allow` lasts until the container restarts; add the domain to\n" +
			"[firewall] allow in .ccic.conf to make it permanent.",
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if !p.compose.Running(svcClaude) {
				return fmt.Errorf("container is not running — run `ccic up` first")
			}
			action := args[0]
			switch action {
			case "on", "off", "status":
				if len(args) > 1 {
					return fmt.Errorf("%s takes no arguments", action)
				}
			case "allow":
				if len(args) < 2 {
					return fmt.Errorf("allow needs at least one domain, IP or CIDR")
				}
			default:
				return fmt.Errorf("unknown action %q", action)
			}
			return p.compose.Exec("root", svcClaude, true,
				append([]string{"/usr/local/bin/ccic-firewall"}, args...)...)
		},
	}
	return cmd
}

func destroyCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "destroy",
		Short: "Remove this project's containers, image and volumes",
		Long:  "The shared base image is never removed; use `ccic prune` for that.",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := load()
			if err != nil {
				return err
			}
			if !yes {
				msg := fmt.Sprintf(
					"This removes the containers, the image and the volumes for %s.\n"+
						"That includes the Claude login", p.cfg.Suffix)
				if p.cfg.Postgres.Enabled {
					msg += fmt.Sprintf(" and the %s database (pg_dump first if you need it)",
						p.cfg.Postgres.Database)
				}
				if err := confirmDestructive(msg + "."); err != nil {
					return err
				}
			}
			if err := p.compose.Run("down", "-v", "--rmi", "local", "--remove-orphans"); err != nil {
				return err
			}
			if err := os.RemoveAll(filepath.Join(p.dir, render.DirName)); err != nil {
				return err
			}
			okay("destroyed — %s and %s are kept", config.FileName, render.DocName)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "skip the confirmation prompt")
	return cmd
}

func pruneCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "prune",
		Short: "Remove ccic base images that nothing is using",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := dock.Available(); err != nil {
				return err
			}
			inUse, err := basesInUse()
			if err != nil {
				return err
			}
			out, err := exec.Command("docker", "images",
				"--filter", "reference="+render.BaseImagePrefix, "--format", "{{.Repository}}:{{.Tag}}").Output()
			if err != nil {
				return err
			}
			var removed, kept int
			for _, ref := range strings.Fields(string(out)) {
				if inUse[ref] {
					kept++
					continue
				}
				if exec.Command("docker", "rmi", ref).Run() == nil {
					info("removed %s", ref)
					removed++
				} else {
					kept++
				}
			}
			if kept > 0 {
				info("kept %d base image(s) still used by a project", kept)
			}
			okay("pruned %d base image(s)", removed)
			return nil
		},
	}
}

func confirmDestructive(message string) error {
	fmt.Fprintln(os.Stderr, message)
	fmt.Fprint(os.Stderr, "Continue? [y/N] ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.ToLower(strings.TrimSpace(line)) != "y" {
		return fmt.Errorf("aborted")
	}
	return nil
}

func note(format string, a ...any) {
	fmt.Printf("  \033[33m•\033[0m "+format+"\n", a...)
}

// basesInUse returns the base images that ccic project images were built from.
//
// This cannot be delegated to `docker rmi`: removing a tag whose image has
// child images does not fail, it silently untags it — which looks like a
// successful prune while quietly breaking every project built on that base.
func basesInUse() (map[string]bool, error) {
	ids, err := exec.Command("docker", "images",
		"--filter", "label=org.ccic.base", "--format", "{{.ID}}").Output()
	if err != nil {
		return nil, err
	}
	inUse := map[string]bool{}
	for _, id := range strings.Fields(string(ids)) {
		ref, err := exec.Command("docker", "image", "inspect", id,
			"--format", `{{ index .Config.Labels "org.ccic.base" }}`).Output()
		if err != nil {
			continue
		}
		if r := strings.TrimSpace(string(ref)); r != "" {
			inUse[r] = true
		}
	}
	return inUse, nil
}
