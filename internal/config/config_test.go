package config

import "testing"

func TestValidate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{"defaults are valid", func(*Config) {}, false},
		{"empty suffix", func(c *Config) { c.Suffix = "" }, true},
		{"uppercase suffix", func(c *Config) { c.Suffix = "Acme" }, true},
		{"underscore suffix", func(c *Config) { c.Suffix = "my_proj" }, true},
		{"relative workspace", func(c *Config) { c.Workspace = "workspace-acme" }, true},
		{"absolute isolation path", func(c *Config) { c.Isolation.Paths = []string{"/etc"} }, true},
		{"escaping isolation path", func(c *Config) { c.Isolation.Paths = []string{"../other"} }, true},
		{"config from a newer ccic", func(c *Config) { c.Version = SchemaVersion + 1 }, true},

		// An isolation volume mounted over the screenshot directory would hide
		// it from the host, silently breaking the only channel Claude has for
		// showing visual output.
		{"isolating the screenshot dir", func(c *Config) {
			c.Isolation.Paths = []string{"tmp/screenshots"}
		}, true},
		{"isolating a parent of the screenshot dir", func(c *Config) {
			c.Isolation.Paths = []string{"tmp"}
		}, true},
		{"isolating a sibling of it is fine", func(c *Config) {
			c.Isolation.Paths = []string{"tmp/cache"}
		}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Default("acme")
			tt.mutate(c)
			err := c.Validate()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Acme":                "acme",
		"my_project":          "my-project",
		"Spin Up Solutions":   "spin-up-solutions",
		"ccic-tool":           "ccic-tool",
		"--weird--name--":     "weird-name",
		"dusty.traktor.2.rek": "dusty-traktor-2-rek",
	} {
		if got := Slug(in); got != want {
			t.Errorf("Slug(%q) = %q, want %q", in, got, want)
		}
	}
}
