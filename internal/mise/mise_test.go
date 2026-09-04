package mise

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real project mise.toml carries [env] directives pointing at files that are
// not in the Docker build context. Copying them through makes `mise install`
// fail at build time, so only [tools] may survive.
func TestExtractToolsDropsEverythingButTools(t *testing.T) {
	dir := t.TempDir()
	content := `[tools]
node = "24.9"
python = "3.14"

[env]
_.source = ".env.local.sh"
_.path = ["{{config_root}}/tools/np-cli"]

[tasks.build]
run = "make"
`
	if err := os.WriteFile(filepath.Join(dir, "mise.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	got, n, err := ExtractTools(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("tool count = %d, want 2", n)
	}
	for _, banned := range []string{"_.source", "_.path", "tasks", ".env.local.sh", "config_root"} {
		if strings.Contains(got, banned) {
			t.Errorf("extracted mise.toml still contains %q:\n%s", banned, got)
		}
	}
	for _, want := range []string{"node", "24.9", "python", "3.14"} {
		if !strings.Contains(got, want) {
			t.Errorf("extracted mise.toml is missing %q:\n%s", want, got)
		}
	}
}

// The Dockerfile always COPYs mise.toml, so a project without one must still
// produce a file rather than an error.
func TestExtractToolsWithoutMiseToml(t *testing.T) {
	got, n, err := ExtractTools(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("tool count = %d, want 0", n)
	}
	if !strings.Contains(got, "[tools]") {
		t.Errorf("want an empty [tools] table, got %q", got)
	}
}
