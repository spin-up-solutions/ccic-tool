package render

import (
	"strings"
	"testing"

	"github.com/spin-up-solutions/ccic-tool/internal/config"
)

// postgres 18 took ownership of the layout below /var/lib/postgresql so that
// `pg_upgrade --link` can work, and refuses to initialise when the pre-18
// .../data path is mounted instead.
func TestPGDataMount(t *testing.T) {
	for version, want := range map[string]string{
		"16":   "/var/lib/postgresql/data",
		"17":   "/var/lib/postgresql/data",
		"17.6": "/var/lib/postgresql/data",
		"18":   "/var/lib/postgresql",
		"18.6": "/var/lib/postgresql",
		"19":   "/var/lib/postgresql",
	} {
		if got := pgDataMount(version); got != want {
			t.Errorf("pgDataMount(%q) = %q, want %q", version, got, want)
		}
	}
}

// The base image tag must depend on what actually goes into the image, and
// nothing else. Keying it to the ccic version meant every release — including
// CLI-only fixes — invalidated every project's base and forced a multi-gigabyte
// rebuild on every machine.
func TestBaseImageRefTracksInputsNotVersion(t *testing.T) {
	base := func(mutate func(*config.Config)) string {
		c := config.Default("acme")
		mutate(c)
		return BaseImageRef(c, 501, 20)
	}
	unchanged := base(func(*config.Config) {})

	if got := base(func(*config.Config) {}); got != unchanged {
		t.Fatalf("tag is not stable across calls: %q vs %q", got, unchanged)
	}
	if !strings.HasPrefix(unchanged, "ccic-base:u501-g20-") {
		t.Errorf("tag %q should carry the uid/gid it was built for", unchanged)
	}

	// Anything baked into the image must change the tag.
	for name, mutate := range map[string]func(*config.Config){
		"base image":         func(c *config.Config) { c.Image.Base = "ubuntu:26.04" },
		"node major":         func(c *config.Config) { c.Image.NodeMajor = "22" },
		"playwright version": func(c *config.Config) { c.Image.PlaywrightVersion = "1.59.0" },
	} {
		if got := base(mutate); got == unchanged {
			t.Errorf("changing the %s did not change the tag (%q)", name, got)
		}
	}

	// Anything that does not affect the image must not.
	if got := BaseImageRef(config.Default("totally-different-project"), 501, 20); got != unchanged {
		t.Errorf("project suffix must not affect the base tag: %q vs %q", got, unchanged)
	}
	// A different host user gets its own base, since uid/gid are baked in.
	if got := BaseImageRef(config.Default("acme"), 1000, 1000); got == unchanged {
		t.Error("a different uid/gid must produce a different tag")
	}
}
