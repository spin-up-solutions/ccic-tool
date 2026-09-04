# ccic — Claude Code in Container

Runs Claude Code in a Docker container with its own toolchain, database and
headless browser, **deliberately kept apart** from the copy of the project you
run on your own machine.

The two environments never share a port, a database, or a compiled dependency.
You keep working on the host as normal; Claude works in the container and reports
back visually by writing screenshots into `tmp/screenshots/`, which is on the
bind mount and so opens directly on your machine.

## Install

macOS, via Homebrew:

```sh
brew install spin-up-solutions/tap/ccic
```

macOS or Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/spin-up-solutions/ccic-tool/main/install.sh | sh
```

The installer verifies the release checksum before installing, picks up
`CCIC_VERSION` and `CCIC_INSTALL_DIR` if you set them, and falls back to
`~/.local/bin` when `/usr/local/bin` is not writable.

Either way, `ccic upgrade` updates in place afterwards.

From source, which needs nothing but Go:

```sh
go build -o bin/ccic ./cmd/ccic
```

The binaries are not notarised, so macOS quarantines anything downloaded through
a browser. The Homebrew cask strips the attribute on install and the script
downloads are unaffected; if you do hit it, run
`xattr -d com.apple.quarantine /usr/local/bin/ccic`.

The generated cask carries Linux URLs too, but Homebrew's cask support on Linux
is limited — the install script is the dependable path there.

## Quick start

```sh
cd ~/code/my-project
ccic init      # prompts for a suffix, postgres, redis, firewall
ccic build     # builds the shared base image once, then a thin project layer
ccic start     # launches Claude in the container
```

`ccic` never builds implicitly — `start` fails if `init` or `build` has not been
run. Controlling when a rebuild happens is the point.

## Commands

| | |
|---|---|
| `init` | set up the project; writes `.ccic.conf`, `.ccic.md`, gitignore entries |
| `build` | build the base image if missing, then the project layer |
| `start` | start the containers and run Claude (`ccic start -- --resume` passes args through) |
| `shell`, `exec`, `psql` | a shell, a one-off command, a database session |
| `up`, `stop`, `logs` | container lifecycle |
| `status`, `doctor` | what is configured/built/running, and what is wrong |
| `firewall on\|off\|status\|allow <domain>` | change egress rules live, no restart |
| `regen` | re-render the build context after editing `.ccic.conf` |
| `destroy` | remove this project's containers, image and volumes |
| `force-rebuild` | rebuild with `--no-cache`; **keeps** your login and database |
| `prune` | remove unused base images |
| `upgrade`, `version` | self-update, build info |

## How it stays out of your way

- **No published ports.** Your dev server and Claude's never collide.
- **Its own database.** Unpublished, on an internal network — reachable from the
  container and nowhere else. Claude can migrate, seed and drop it freely.
- **Container-only build artefacts.** `node_modules` and friends are backed by
  named volumes, because native extensions built in the container are Linux and
  yours are macOS. Bundler, uv, pip and cargo are redirected out of the workspace
  entirely.
- **Commit yes, push no.** The host git identity is forwarded, but no credentials
  are — so `git push` cannot authenticate.
- **Egress firewall**, on by default, since `--dangerously-skip-permissions` is
  too. `WebFetch` to a domain outside the allowlist fails fast; `ccic firewall
  allow <domain>` opens one in a second, `ccic firewall off` opens everything for
  a research session.

One shared base image serves every project on the machine; per-project layers
build in seconds.

## Configuration

`.ccic.conf` (TOML) is written by `ccic init`, safe to commit, and documented in
[`plans/1_build_plan.md`](plans/1_build_plan.md) §8. Run `ccic regen` after
editing, or `ccic build` to rebuild with the changes.

`.ccic.md` is generated alongside it and imported into `CLAUDE.md` via a single
`@.ccic.md` line, so Claude knows what the container gives it and what it does
not. ccic never owns your `CLAUDE.md`.
