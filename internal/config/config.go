// Package config defines the .ccic.conf schema and its defaults.
package config

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// FileName is the per-project config, committable but not required.
const FileName = ".ccic.conf"

// SchemaVersion lets a future ccic migrate an old file rather than choke on it.
const SchemaVersion = 1

type Config struct {
	Version   int    `toml:"version" comment:"ccic config schema version"`
	Suffix    string `toml:"suffix"`
	Workspace string `toml:"workspace"`

	Image     Image     `toml:"image"`
	Postgres  Postgres  `toml:"postgres"`
	Redis     Redis     `toml:"redis"`
	Isolation Isolation `toml:"isolation"`
	Network   Network   `toml:"network"`
	Firewall  Firewall  `toml:"firewall"`
	Git       Git       `toml:"git"`
	Env       Env       `toml:"env"`
	Claude    Claude    `toml:"claude"`
}

type Image struct {
	Base              string   `toml:"base"`
	AptPackages       []string `toml:"apt_packages"`
	NodeMajor         string   `toml:"node_major"`
	PlaywrightVersion string   `toml:"playwright_version"`
}

type Postgres struct {
	Enabled  bool   `toml:"enabled"`
	Version  string `toml:"version"`
	Database string `toml:"database"`
	User     string `toml:"user"`
	Password string `toml:"password"`
}

type Redis struct {
	Enabled bool   `toml:"enabled"`
	Version string `toml:"version"`
}

// Isolation keeps the host's and the container's build artefacts apart. Paths
// listed here are backed by named volumes, so native extensions compiled in the
// container (Linux) never mix with the host's (macOS).
type Isolation struct {
	Paths       []string `toml:"paths"`
	Screenshots string   `toml:"screenshots"`
}

// Network.Publish is deliberately empty by default: the host runs its own copy
// of the project and the container must never compete for a port.
type Network struct {
	Publish []int `toml:"publish"`
}

type Firewall struct {
	Enabled bool     `toml:"enabled"`
	Allow   []string `toml:"allow"`
}

type Git struct {
	Identity  bool `toml:"identity"`
	AllowPush bool `toml:"allow_push"`
}

type Env struct {
	Passthrough []string `toml:"passthrough"`
	File        string   `toml:"file"`
}

type Claude struct {
	SkipPermissions bool     `toml:"skip_permissions"`
	ExtraArgs       []string `toml:"extra_args"`
}

// PostgresVersions is offered at init; the last entry is the default.
var PostgresVersions = []string{"16", "17", "18"}

func Default(suffix string) *Config {
	return &Config{
		Version:   SchemaVersion,
		Suffix:    suffix,
		Workspace: "/workspace-" + suffix,
		Image: Image{
			Base:              "ubuntu:24.04",
			AptPackages:       []string{},
			NodeMajor:         "24",
			PlaywrightVersion: "1.58.0",
		},
		Postgres: Postgres{
			Enabled:  true,
			Version:  PostgresVersions[len(PostgresVersions)-1],
			Database: suffix + "_development",
			User:     "postgres",
			Password: "postgres",
		},
		Redis:     Redis{Enabled: false, Version: "8"},
		Isolation: Isolation{Paths: []string{"node_modules"}, Screenshots: "tmp/screenshots"},
		Network:   Network{Publish: []int{}},
		Firewall:  Firewall{Enabled: true, Allow: []string{}},
		Git:       Git{Identity: true, AllowPush: false},
		Env:       Env{Passthrough: []string{}, File: ".ccic.local.env"},
		Claude:    Claude{SkipPermissions: true, ExtraArgs: []string{}},
	}
}

var suffixRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

func Slug(s string) string {
	s = strings.ToLower(s)
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}

func (c *Config) Validate() error {
	if c.Suffix == "" {
		return fmt.Errorf("suffix must not be empty")
	}
	if !suffixRe.MatchString(c.Suffix) {
		return fmt.Errorf("suffix %q must be lowercase letters, digits and dashes", c.Suffix)
	}
	if c.Version > SchemaVersion {
		return fmt.Errorf("%s was written by a newer ccic (schema v%d, this build understands v%d)",
			FileName, c.Version, SchemaVersion)
	}
	if !strings.HasPrefix(c.Workspace, "/") {
		return fmt.Errorf("workspace %q must be an absolute path", c.Workspace)
	}
	for _, p := range c.Isolation.Paths {
		if strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			return fmt.Errorf("isolation path %q must be relative to the workspace", p)
		}
		// Overlaying the screenshot directory would hide it from the host, which
		// breaks the only channel Claude has for showing visual output.
		if c.Isolation.Screenshots != "" &&
			(p == c.Isolation.Screenshots || strings.HasPrefix(c.Isolation.Screenshots, p+"/")) {
			return fmt.Errorf("isolation path %q would hide the screenshot directory %q from the host",
				p, c.Isolation.Screenshots)
		}
	}
	return nil
}

// Path returns the config file location for a project directory.
func Path(dir string) string { return filepath.Join(dir, FileName) }

func Load(dir string) (*Config, error) {
	b, err := os.ReadFile(Path(dir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no %s in %s — run `ccic init` first", FileName, dir)
		}
		return nil, err
	}
	// Start from defaults so a config written by an older ccic picks up new keys.
	c := Default("")
	if err := toml.Unmarshal(b, c); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", FileName, err)
	}
	if c.Workspace == "" && c.Suffix != "" {
		c.Workspace = "/workspace-" + c.Suffix
	}
	if c.Postgres.Database == "" && c.Suffix != "" {
		c.Postgres.Database = c.Suffix + "_development"
	}
	return c, c.Validate()
}

func (c *Config) Save(dir string) error {
	b, err := toml.Marshal(c)
	if err != nil {
		return err
	}
	header := "# ccic project configuration.\n" +
		"# Safe to commit. Regenerate the build context with `ccic build` after editing.\n\n"
	return os.WriteFile(Path(dir), append([]byte(header), b...), 0o644)
}

// HostTimezone reads the host's zone so container logs match the wall clock.
func HostTimezone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if p, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			return p[i+len("zoneinfo/"):]
		}
	}
	return "UTC"
}

// GitIdentity reads the host's git identity so commits made in the container
// are attributed correctly, without mounting any host credentials.
func GitIdentity(dir string) (name, email string) {
	get := func(key string) string {
		out, err := exec.Command("git", "-C", dir, "config", "--get", key).Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	return get("user.name"), get("user.email")
}
