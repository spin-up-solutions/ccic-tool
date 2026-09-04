// Package render turns a Config into the .ccic build context.
package render

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/spin-up-solutions/ccic-tool/internal/config"
	"github.com/spin-up-solutions/ccic-tool/internal/mise"
)

// DirName is the generated build context, gitignored and disposable.
const DirName = ".ccic"

// DocName is the Claude-facing documentation, committable and imported by
// CLAUDE.md via `@.ccic.md`.
const DocName = ".ccic.md"

// User is the unprivileged account inside the container.
const User = "dev"

// reservedEnv lists every environment key the compose template sets itself.
// Keep in sync with templates/compose.yml.tmpl.
var reservedEnv = map[string]bool{
	"CCIC_USER": true, "CCIC_WORKSPACE": true, "CCIC_SCREENSHOT_DIR": true,
	"CCIC_CHOWN_PATHS": true, "CCIC_FIREWALL": true, "CCIC_FIREWALL_ALLOW": true,
	"CLAUDE_CONFIG_DIR": true, "MISE_TRUSTED_CONFIG_PATHS": true, "TZ": true,
	"GIT_AUTHOR_NAME": true, "GIT_AUTHOR_EMAIL": true,
	"GIT_COMMITTER_NAME": true, "GIT_COMMITTER_EMAIL": true,
	"BUNDLE_PATH": true, "BUNDLE_APP_CONFIG": true, "UV_PROJECT_ENVIRONMENT": true,
	"PIP_CACHE_DIR": true, "CARGO_TARGET_DIR": true, "npm_config_cache": true,
	"DATABASE_URL": true, "PGHOST": true, "PGUSER": true, "PGPASSWORD": true,
	"PGDATABASE": true, "REDIS_URL": true,
}

type IsoVolume struct {
	Volume    string
	MountPath string
}

// View is the data handed to the templates: the config plus everything derived
// from the host that the templates should not work out for themselves.
type View struct {
	Cfg              *config.Config
	Version          string
	HostDir          string
	User             string
	UID, GID         int
	ImageName        string
	BaseImage        string
	Timezone         string
	GitName          string
	GitEmail         string
	EnvFile          string
	ScreenshotDir    string
	ChownPaths       string
	FirewallAllow    string
	DatabaseURL      string
	PGDataMount      string
	IsolationVolumes []IsoVolume
	PassthroughEnv   []string
	MiseTools        []string
	NodeMajor        string
}

// BaseImageRef derives the shared base image tag from the content of the base
// Dockerfile and the arguments it is built with.
//
// Deliberately NOT keyed to the ccic version. Doing that invalidated every
// project's base image on every release — including CLI-only bugfixes that do
// not touch the image at all — forcing a multi-gigabyte rebuild on every
// machine for no reason, which defeats most of the point of sharing a base.
// Hashing the real inputs means the base is rebuilt exactly when its definition
// changes, and reused otherwise.
//
// uid/gid appear in the tag as well as the hash because they are baked into the
// image and it helps to see which user a given base belongs to.
func BaseImageRef(c *config.Config, uid, gid int) string {
	h := sha256.New()
	if df, err := assets.ReadFile("templates/Dockerfile.base"); err == nil {
		h.Write(df)
	}
	fmt.Fprintf(h, "\x00%s\x00%s\x00%s\x00%s\x00%d\x00%d",
		c.Image.Base, c.Image.NodeMajor, c.Image.PlaywrightVersion, User, uid, gid)
	return fmt.Sprintf("ccic-base:u%d-g%d-%s", uid, gid, hex.EncodeToString(h.Sum(nil))[:12])
}

// BaseImagePrefix matches every base image ccic has ever built, for pruning.
const BaseImagePrefix = "ccic-base"

func ProjectName(c *config.Config) string { return "ccic-" + c.Suffix }
func ImageRef(c *config.Config) string    { return "ccic-" + c.Suffix + ":latest" }

// pgDataMount is version-dependent: postgres 18 took ownership of the layout
// below /var/lib/postgresql so that `pg_upgrade --link` can work, and refuses
// to initialise when the pre-18 .../data path is mounted instead.
func pgDataMount(version string) string {
	major := version
	if i := strings.IndexByte(major, '.'); i > 0 {
		major = major[:i]
	}
	if major >= "18" && len(major) >= 2 {
		return "/var/lib/postgresql"
	}
	return "/var/lib/postgresql/data"
}

func NewView(c *config.Config, hostDir, version string, uid, gid int) *View {
	name, email := "", ""
	if c.Git.Identity {
		name, email = config.GitIdentity(hostDir)
	}

	vols := make([]IsoVolume, 0, len(c.Isolation.Paths))
	chown := make([]string, 0, len(c.Isolation.Paths))
	for _, p := range c.Isolation.Paths {
		mount := c.Workspace + "/" + strings.TrimPrefix(p, "./")
		vols = append(vols, IsoVolume{Volume: "iso-" + config.Slug(p), MountPath: mount})
		chown = append(chown, mount)
	}

	pass := make([]string, 0, len(c.Env.Passthrough))
	var shadowed []string
	for _, k := range c.Env.Passthrough {
		// ccic sets these itself. Passing one through as well would emit the
		// key twice and produce an invalid compose file, so drop it and say so.
		if reservedEnv[k] {
			shadowed = append(shadowed, k)
			continue
		}
		// Compose substitutes from ccic's own environment at `up` time, which
		// keeps secrets out of the generated file.
		pass = append(pass, fmt.Sprintf("%s: ${%s:-}", k, k))
	}
	if len(shadowed) > 0 {
		fmt.Fprintf(os.Stderr,
			"\033[33mccic:\033[0m ignoring [env] passthrough %s — ccic sets %s itself\n",
			strings.Join(shadowed, ", "),
			map[bool]string{true: "these", false: "it"}[len(shadowed) > 1])
	}

	dbURL := ""
	if c.Postgres.Enabled {
		dbURL = fmt.Sprintf("postgres://%s:%s@db:5432/%s",
			c.Postgres.User, c.Postgres.Password, c.Postgres.Database)
	}

	envFile := c.Env.File
	if envFile != "" && !filepath.IsAbs(envFile) {
		envFile = filepath.Join(hostDir, envFile)
	}

	return &View{
		Cfg: c, Version: version, HostDir: hostDir, User: User, UID: uid, GID: gid,
		ImageName:        ImageRef(c),
		BaseImage:        BaseImageRef(c, uid, gid),
		Timezone:         config.HostTimezone(),
		GitName:          name,
		GitEmail:         email,
		EnvFile:          envFile,
		ScreenshotDir:    c.Workspace + "/" + c.Isolation.Screenshots,
		ChownPaths:       strings.Join(chown, " "),
		FirewallAllow:    strings.Join(c.Firewall.Allow, " "),
		DatabaseURL:      dbURL,
		PGDataMount:      pgDataMount(c.Postgres.Version),
		IsolationVolumes: vols,
		PassthroughEnv:   pass,
		MiseTools:        mise.ToolNames(hostDir),
		NodeMajor:        c.Image.NodeMajor,
	}
}

// Context writes the build context: static assets, the rendered compose file
// and a tools-only mise.toml.
func (v *View) Context() error {
	ctx := filepath.Join(v.HostDir, DirName)
	if err := os.MkdirAll(ctx, 0o755); err != nil {
		return err
	}
	for _, f := range []string{"Dockerfile.project", "entrypoint.sh", "init-firewall.sh"} {
		b, err := assets.ReadFile("templates/" + f)
		if err != nil {
			return err
		}
		mode := os.FileMode(0o644)
		if strings.HasSuffix(f, ".sh") {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(ctx, f), b, mode); err != nil {
			return err
		}
	}

	tools, _, err := mise.ExtractTools(v.HostDir)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(ctx, "mise.toml"), []byte(tools), 0o644); err != nil {
		return err
	}

	out, err := v.execute("templates/compose.yml.tmpl")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(ctx, "compose.yml"), out, 0o644)
}

// Doc writes .ccic.md and makes sure CLAUDE.md imports it. ccic never owns
// CLAUDE.md; it only adds a single `@.ccic.md` line.
func (v *View) Doc() error {
	out, err := v.execute("templates/ccic.md.tmpl")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(v.HostDir, DocName), out, 0o644); err != nil {
		return err
	}
	return v.ensureImport()
}

func (v *View) ensureImport() error {
	path := filepath.Join(v.HostDir, "CLAUDE.md")
	imp := "@" + DocName
	b, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.TrimSpace(line) == imp {
			return nil
		}
	}
	var buf bytes.Buffer
	buf.Write(b)
	if len(bytes.TrimSpace(b)) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString(imp + "\n")
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func (v *View) execute(name string) ([]byte, error) {
	t, err := template.New(filepath.Base(name)).ParseFS(assets, name)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// BaseDockerfile writes the base Dockerfile to a scratch directory and returns
// its path plus the (empty) build context to use.
func BaseDockerfile(dir string) (string, error) {
	b, err := assets.ReadFile("templates/Dockerfile.base")
	if err != nil {
		return "", err
	}
	p := filepath.Join(dir, "Dockerfile.base")
	return p, os.WriteFile(p, b, 0o644)
}
