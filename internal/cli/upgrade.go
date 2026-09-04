package cli

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

func upgradeCmd() *cobra.Command {
	var force, checkOnly bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Update ccic to the latest release",
		Long: "Downloads the latest release from GitHub, verifies its checksum and\n" +
			"replaces this binary in place.\n\n" +
			Repo + " is private, so this uses the `gh` CLI's credentials when it\n" +
			"is installed and signed in. Without gh, only public releases are reachable.",
		RunE: func(cmd *cobra.Command, args []string) error {
			self, err := os.Executable()
			if err != nil {
				return err
			}
			if self, err = filepath.EvalSymlinks(self); err != nil {
				return err
			}
			// Homebrew owns its own install; upgrading underneath it would be
			// overwritten by the next `brew upgrade` and confuse the receipt.
			if strings.Contains(self, "/Cellar/") || strings.Contains(self, "/homebrew/") {
				return fmt.Errorf("this ccic was installed by Homebrew — run `brew upgrade ccic` instead")
			}

			latest, err := latestTag()
			if err != nil {
				return err
			}
			current := "v" + strings.TrimPrefix(Version, "v")
			if latest == current && !force {
				okay("ccic %s is already the latest release", Version)
				return nil
			}
			if checkOnly {
				info("current %s, latest %s — run `ccic upgrade` to update", current, latest)
				return nil
			}
			info("updating %s → %s", current, latest)

			tmp, err := os.MkdirTemp("", "ccic-upgrade-")
			if err != nil {
				return err
			}
			defer os.RemoveAll(tmp)

			asset := fmt.Sprintf("ccic_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
			if err := download(latest, asset, tmp); err != nil {
				return err
			}
			if err := download(latest, "checksums.txt", tmp); err != nil {
				return err
			}
			if err := verify(filepath.Join(tmp, asset), filepath.Join(tmp, "checksums.txt"), asset); err != nil {
				return err
			}
			bin, err := extract(filepath.Join(tmp, asset), tmp)
			if err != nil {
				return err
			}
			if err := replace(self, bin); err != nil {
				return err
			}
			okay("ccic is now %s", latest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "reinstall even if already up to date")
	cmd.Flags().BoolVar(&checkOnly, "check", false, "report whether an update exists, change nothing")
	return cmd
}

func ghAvailable() bool {
	if _, err := exec.LookPath("gh"); err != nil {
		return false
	}
	return exec.Command("gh", "auth", "status").Run() == nil
}

// latestTag prefers the gh CLI, which carries credentials for a private repo.
//
// When gh is present and signed in it is authoritative: falling back to the
// public API would turn "this repo has no releases yet" into a misleading
// "install gh", which is exactly the wrong thing to tell someone who has it.
func latestTag() (string, error) {
	if ghAvailable() {
		out, err := exec.Command("gh", "release", "view",
			"--repo", Repo, "--json", "tagName", "--jq", ".tagName").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("no release found in %s: %s",
				Repo, firstLine(strings.TrimSpace(string(out))))
		}
		tag := strings.TrimSpace(string(out))
		if tag == "" {
			return "", fmt.Errorf("%s has no releases yet", Repo)
		}
		return tag, nil
	}
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get("https://api.github.com/repos/" + Repo + "/releases/latest")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", fmt.Errorf("no public release found for %s — install the `gh` CLI and run `gh auth login` "+
			"so ccic can read the private repository", Repo)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("github returned %s", resp.Status)
	}
	var body struct {
		TagName string `json:"tag_name"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	return body.TagName, nil
}

func download(tag, pattern, dir string) error {
	if ghAvailable() {
		return exec.Command("gh", "release", "download", tag,
			"--repo", Repo, "--pattern", pattern, "--dir", dir, "--clobber").Run()
	}
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/%s", Repo, tag, pattern)
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", pattern, resp.Status)
	}
	f, err := os.Create(filepath.Join(dir, pattern))
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(f, resp.Body)
	return err
}

func verify(archive, sums, name string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))

	b, err := os.ReadFile(sums)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == name {
			if fields[0] != got {
				return fmt.Errorf("checksum mismatch for %s — refusing to install", name)
			}
			return nil
		}
	}
	return fmt.Errorf("no checksum listed for %s — refusing to install", name)
}

func extract(archive, dir string) (string, error) {
	f, err := os.Open(archive)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "ccic" {
			continue
		}
		out := filepath.Join(dir, "ccic.new")
		w, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return "", err
		}
		//nolint:gosec // archive is checksum-verified above
		if _, err := io.Copy(w, tr); err != nil {
			w.Close()
			return "", err
		}
		w.Close()
		return out, nil
	}
	return "", fmt.Errorf("no ccic binary inside the release archive")
}

// replace swaps the running binary. Rename keeps the running process on its own
// inode, so the swap is atomic and the current invocation survives it.
func replace(self, next string) error {
	staged := self + ".new"
	in, err := os.Open(next)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		if os.IsPermission(err) {
			return fmt.Errorf("cannot write to %s — re-run with sudo, or install ccic somewhere you own",
				filepath.Dir(self))
		}
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(staged)
		return err
	}
	out.Close()

	if err := os.Rename(staged, self); err != nil {
		os.Remove(staged)
		return fmt.Errorf("replacing %s: %w", self, err)
	}
	return nil
}
