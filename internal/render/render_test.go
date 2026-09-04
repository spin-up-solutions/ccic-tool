package render

import "testing"

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
