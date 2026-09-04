// Package dock wraps the docker CLI.
//
// Shelling out rather than using the SDK is deliberate: it inherits the user's
// docker context, credential helpers, BuildKit configuration and DOCKER_HOST
// for free, and `docker compose down -v` implements most of `destroy`.
package dock

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

type Compose struct {
	ProjectName string // -p
	Dir         string // --project-directory, holds compose.yml
}

func run(name string, args []string, interactive bool) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if interactive {
		cmd.Stdin = os.Stdin
	}
	return cmd.Run()
}

func capture(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// Available reports whether the docker daemon is reachable.
func Available() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return errors.New("docker not found on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		return errors.New("docker daemon is not responding — is Docker running?")
	}
	return nil
}

func ImageExists(ref string) bool {
	return exec.Command("docker", "image", "inspect", ref).Run() == nil
}

func ImageCreated(ref string) string {
	s, err := capture("docker", "image", "inspect", "-f", "{{.Created}}", ref)
	if err != nil {
		return ""
	}
	return s
}

// Build builds an image from an explicit Dockerfile with a given context.
func Build(dockerfile, context, tag string, buildArgs map[string]string, noCache bool) error {
	args := []string{"build", "-f", dockerfile, "-t", tag}
	if noCache {
		args = append(args, "--no-cache")
	}
	for k, v := range buildArgs {
		args = append(args, "--build-arg", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args, context)
	return run("docker", args, false)
}

func (c Compose) args(rest ...string) []string {
	return append([]string{"compose", "--project-directory", c.Dir, "-p", c.ProjectName}, rest...)
}

func (c Compose) Run(rest ...string) error    { return run("docker", c.args(rest...), false) }
func (c Compose) RunTTY(rest ...string) error { return run("docker", c.args(rest...), true) }

func (c Compose) Capture(rest ...string) (string, error) {
	return capture("docker", c.args(rest...)...)
}

// Exec runs a command in a service as the given user.
//
// Claude Code refuses --dangerously-skip-permissions when launched as root, and
// the container's PID 1 is root so the entrypoint can manage iptables — so
// every interactive exec must name the unprivileged user explicitly.
func (c Compose) Exec(user, service string, tty bool, cmd ...string) error {
	args := []string{"exec", "-u", user}
	if !tty {
		args = append(args, "-T")
	}
	args = append(args, service)
	args = append(args, cmd...)
	return run("docker", c.args(args...), tty)
}

func (c Compose) ExecCapture(user, service string, cmd ...string) (string, error) {
	args := append([]string{"exec", "-T", "-u", user, service}, cmd...)
	return capture("docker", c.args(args...)...)
}

// Running reports whether a service has a running container.
func (c Compose) Running(service string) bool {
	out, err := c.Capture("ps", "--status", "running", "--quiet", service)
	return err == nil && out != ""
}

// ReplaceProcess hands the terminal over to docker for an interactive session.
func ReplaceProcess(args []string) error {
	return run("docker", args, true)
}

// RunQuiet discards output. Used when the stack is already up, where compose's
// per-container "Running / Waiting / Healthy" chatter is pure noise.
func (c Compose) RunQuiet(rest ...string) error {
	return exec.Command("docker", c.args(rest...)...).Run()
}
