package cli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"

	"github.com/spin-up-solutions/ccic-tool/internal/config"
	"github.com/spin-up-solutions/ccic-tool/internal/render"
)

func initCmd() *cobra.Command {
	var (
		nonInteractive bool
		suffix         string
		withPostgres   bool
		pgVersion      string
		withRedis      bool
		firewall       bool
		force          bool
	)
	cmd := &cobra.Command{
		Use:   "init",
		Short: "Set up the current folder as a ccic sandbox",
		Long: "Writes .ccic.conf, generates .ccic.md, adds an @.ccic.md import to\n" +
			"CLAUDE.md and updates .gitignore. Does not build anything.",
		RunE: func(cmd *cobra.Command, args []string) error {
			d, err := dir()
			if err != nil {
				return err
			}
			if _, err := os.Stat(config.Path(d)); err == nil && !force {
				return fmt.Errorf("%s already exists in %s (use --force to overwrite)", config.FileName, d)
			}

			cfg := config.Default(config.Slug(filepath.Base(d)))
			if cmd.Flags().Changed("suffix") {
				cfg.Suffix = config.Slug(suffix)
			}
			if cmd.Flags().Changed("postgres") {
				cfg.Postgres.Enabled = withPostgres
			}
			if cmd.Flags().Changed("pg-version") {
				cfg.Postgres.Version = pgVersion
			}
			if cmd.Flags().Changed("redis") {
				cfg.Redis.Enabled = withRedis
			}
			if cmd.Flags().Changed("firewall") {
				cfg.Firewall.Enabled = firewall
			}

			if !nonInteractive {
				if err := promptInit(cfg); err != nil {
					return err
				}
			}
			// Suffix drives both, so re-derive after any prompt changed it.
			cfg.Workspace = "/workspace-" + cfg.Suffix
			cfg.Postgres.Database = cfg.Suffix + "_development"

			if err := cfg.Validate(); err != nil {
				return err
			}
			if err := cfg.Save(d); err != nil {
				return err
			}
			okay("wrote %s", config.Path(d))

			v := render.NewView(cfg, d, Version, os.Getuid(), os.Getgid())
			if err := v.Doc(); err != nil {
				return err
			}
			okay("wrote %s and imported it from CLAUDE.md", filepath.Join(d, render.DocName))

			if err := ensureGitignore(d, cfg); err != nil {
				return err
			}
			info("workspace will be %s", cfg.Workspace)
			info("next: ccic build")
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&nonInteractive, "non-interactive", false, "skip prompts and use flags/defaults")
	f.StringVar(&suffix, "suffix", "", "project suffix (workspace becomes /workspace-<suffix>)")
	f.BoolVar(&withPostgres, "postgres", true, "include a postgres container")
	f.StringVar(&pgVersion, "pg-version", config.PostgresVersions[len(config.PostgresVersions)-1], "postgres major version")
	f.BoolVar(&withRedis, "redis", false, "include a redis container")
	f.BoolVar(&firewall, "firewall", true, "restrict outbound traffic to an allowlist")
	f.BoolVar(&force, "force", false, "overwrite an existing .ccic.conf")
	return cmd
}

func promptInit(cfg *config.Config) error {
	pgOptions := make([]huh.Option[string], 0, len(config.PostgresVersions))
	for _, v := range slices.Backward(config.PostgresVersions) {
		pgOptions = append(pgOptions, huh.NewOption("PostgreSQL "+v, v))
	}

	form := huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("Project suffix").
				Description("The workspace inside the container becomes /workspace-<suffix>.").
				Value(&cfg.Suffix).
				Validate(func(s string) error {
					c := *cfg
					c.Suffix = config.Slug(s)
					c.Workspace = "/workspace-" + c.Suffix
					return c.Validate()
				}),
			huh.NewConfirm().
				Title("Add a PostgreSQL container?").
				Description("Reachable only from the Claude container, never from your host.").
				Value(&cfg.Postgres.Enabled),
		),
		huh.NewGroup(
			huh.NewSelect[string]().
				Title("PostgreSQL version").
				Options(pgOptions...).
				Value(&cfg.Postgres.Version),
		).WithHideFunc(func() bool { return !cfg.Postgres.Enabled }),
		huh.NewGroup(
			huh.NewConfirm().
				Title("Add a Redis container?").
				Value(&cfg.Redis.Enabled),
			huh.NewConfirm().
				Title("Restrict outbound network traffic?").
				Description("Recommended with --dangerously-skip-permissions. WebFetch to\n"+
					"domains outside the allowlist will fail; `ccic firewall allow <domain>`\n"+
					"or `ccic firewall off` opens it up when you need it.").
				Value(&cfg.Firewall.Enabled),
		),
	)
	if err := form.Run(); err != nil {
		return err
	}
	cfg.Suffix = config.Slug(cfg.Suffix)
	return nil
}

// ensureGitignore adds the generated paths without disturbing existing entries.
func ensureGitignore(d string, cfg *config.Config) error {
	want := []string{render.DirName + "/", cfg.Isolation.Screenshots + "/"}
	if cfg.Env.File != "" {
		want = append(want, cfg.Env.File)
	}

	path := filepath.Join(d, ".gitignore")
	existing := map[string]bool{}
	if f, err := os.Open(path); err == nil {
		s := bufio.NewScanner(f)
		for s.Scan() {
			existing[strings.TrimSpace(s.Text())] = true
		}
		f.Close()
	}

	var add []string
	for _, w := range want {
		if !existing[w] {
			add = append(add, w)
		}
	}
	if len(add) == 0 {
		return nil
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.WriteString("\n# ccic\n" + strings.Join(add, "\n") + "\n"); err != nil {
		return err
	}
	info("added to .gitignore: %s", strings.Join(add, " "))
	return nil
}
