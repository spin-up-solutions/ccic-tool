# `ccic` — Build Plan

Companion to [`0_initial_plan.md`](./0_initial_plan.md). That document is the requirement;
this one is the design, the stack decision, and the delivery order.

Revision 2 — incorporates the separation principle, shared base image, egress firewall,
push-blocking, and the removal of published ports.

---

## 1. The governing principle: separation

The container is **not a second copy of your dev environment**. It is a peer that happens to
see the same files. You run the app on the host; Claude runs its own instance inside the
container purely so it can drive a headless browser and run tests. The two must never
contend for a port, a database, or a compiled artefact.

That gives three boundaries, and only the third one is difficult:

| Boundary | Mechanism | Status |
|---|---|---|
| **Network** | No published ports at all. The container has its own network namespace, so Claude's dev server on `:3000` and yours on `:3000` cannot collide. | easy |
| **Data** | Container postgres is unpublished and separate from your host postgres. Claude's migrations hit its own database; the resulting `schema.rb` lands on the bind mount, which is what you want. | easy |
| **Build artefacts** | `node_modules`, `vendor/bundle`, `.venv`, `target/` live *on the shared bind mount* and contain platform-specific native code. macOS-compiled gems will not load on Linux, and vice versa. Without intervention the two environments actively destroy each other's installs. | **needs work — §4** |

The channel back to you is the filesystem, not HTTP: Claude writes screenshots into
`tmp/screenshots/` on the bind mount, and you open them on the host. No port forwarding,
no shared services, no coordination.

> **Reversal from revision 1:** published ports are now *off by default and discouraged*.
> `[network] publish = []` remains as an escape hatch, documented as deliberately empty.

---

## 2. What the tool is

A **config-file-driven generator + `docker compose` wrapper**:

```
.ccic.conf  ──▶  ccic build  ──▶  .ccic/            ──▶  docker compose -p ccic-<suffix>
(committed)                        ├── Dockerfile          (+ shared ccic-base image)
                                   ├── compose.yml
                                   ├── entrypoint.sh
                                   ├── init-firewall.sh
                                   └── mise.toml (copied or stubbed)
```

The real product is the images + compose topology + config schema. Those are
language-agnostic; the CLI is a few hundred lines of "read TOML, render templates, exec
docker". Which is why [Phase 0](#9-delivery-phases) proves the containers by hand before a
line of CLI gets written.

Shell out to the `docker` CLI rather than using a Docker SDK — it inherits your docker
context, credential helpers, BuildKit config and `DOCKER_HOST` for free, and
`docker compose down -v` implements most of `destroy`.

---

## 3. Stack options

| | **Go** | **Bun** | **Rust** |
|---|---|---|---|
| Binary size | ~6–10 MB | ~57–100 MB | ~3–5 MB |
| Cross-compile darwin/linux × arm64/x64 | `GOOS`/`GOARCH`, zero setup | `--target=bun-linux-arm64`, zero setup | needs `cross`/zig-cc from macOS |
| Release pipeline | **GoReleaser** — binaries, checksums, GH release, Homebrew tap from one YAML | hand-rolled GH Actions matrix | `cargo-dist` |
| Interactive `init` | `charmbracelet/huh` | `@clack/prompts` | `inquire` |
| Embedded templates | `go:embed` | string literals / `Bun.embeddedFiles` | `include_str!` |
| Time to first working version | low | lowest | highest |

**Still recommending Go**, on distribution grounds: GoReleaser does the entire "build a
binary, push it to GitHub releases, distribute from there" requirement from a single YAML,
and 7 MB beats 60 MB for something you install on every machine. Bun is a legitimate second
choice — fastest prototype, `Bun.$` makes docker shelling pleasant — at the cost of binary
size and a hand-built release matrix.

**Decided: Go.** (Phase 0 was language-agnostic and is complete either way.)

---

## 4. Keeping the two environments apart

The critical mechanism. Two techniques, applied per ecosystem:

**a. Redirect out of the workspace via env vars** — preferred, no volume needed, the paths
land in the container's `~/.cache` volume and survive rebuilds:

| Ecosystem | Variable | Value |
|---|---|---|
| Ruby / Bundler | `BUNDLE_PATH` | `/home/dev/.cache/bundle` |
| Ruby | `BUNDLE_APP_CONFIG` | `/home/dev/.bundle` |
| Python / uv | `UV_PROJECT_ENVIRONMENT` | `/home/dev/.cache/venv` |
| Python / pip | `PIP_CACHE_DIR` | `/home/dev/.cache/pip` |
| Rust | `CARGO_TARGET_DIR` | `/home/dev/.cache/cargo-target` |
| Go | `GOPATH`, `GOCACHE` | `/home/dev/.cache/go` |

**b. Overlay a named volume** — for paths that *must* sit inside the project because the
resolver demands it. `node_modules` is the main one; Node's resolution algorithm walks up
from the file, so it cannot be redirected.

```yaml
volumes:
  - ${HOST_PROJECT_DIR}:/workspace-acme:delegated
  - nodemods:/workspace-acme/node_modules   # container's copy, host's is hidden
```

The volume starts empty, Claude runs `npm install` into it, and the host's `node_modules`
is untouched and invisible from inside. Exactly the isolation we want.

**Do not overlay `tmp/`.** Screenshots live at `tmp/screenshots/` and must stay on the bind
mount so you can open them on the host. If a project needs `tmp/cache` isolated, name that
path specifically.

```toml
[isolation]
paths = ["node_modules"]        # add ".venv", "target", "tmp/cache" per project
```

A pleasant side effect: because the project's own Playwright (if it has one) lives in the
isolated `node_modules`, it can never conflict with ccic's Playwright. The version-skew
problem from revision 1 dissolves.

---

## 5. Container topology

```
                     ┌───────────────────────────────────┐
                     │  claude container                 │
   (no host ports)   │  ubuntu 24.04 + mise + claude     │──▶ internet, via egress firewall
                     └──────┬────────────────────────────┘
                            │  internal  (internal: true — no route off-host)
                     ┌──────┴──────────┐
                     │  postgres:18    │   no published ports, no internet
                     └─────────────────┘
```

```yaml
# .ccic/compose.yml (generated)
name: ccic-${SUFFIX}

networks:
  internal: { internal: true }   # db only: no egress, no host access
  egress:   {}                   # claude also: internet

services:
  db:
    image: postgres:${PG_VERSION}-bookworm
    networks: [internal]         # ← no `ports:` key. Unreachable from the host.
    environment:
      POSTGRES_USER: postgres
      POSTGRES_PASSWORD: postgres
      POSTGRES_DB: ${SUFFIX}_development
    volumes: [pgdata:/var/lib/postgresql/data]
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U postgres"]
      interval: 2s
      retries: 30

  claude:
    image: ccic-${SUFFIX}:latest
    build: { context: ., dockerfile: Dockerfile }
    networks: [internal, egress]
    depends_on: { db: { condition: service_healthy } }
    working_dir: /workspace-${SUFFIX}
    cap_add: [NET_ADMIN, NET_RAW]        # for the egress firewall
    init: true
    command: ["sleep", "infinity"]
    volumes:
      - ${HOST_PROJECT_DIR}:/workspace-${SUFFIX}:delegated
      - claude-config:/home/dev/.claude   # ← see note
      - caches:/home/dev/.cache
      - nodemods:/workspace-${SUFFIX}/node_modules
    environment:
      CLAUDE_CONFIG_DIR: /home/dev/.claude
      DATABASE_URL: postgres://postgres:postgres@db:5432/${SUFFIX}_development
      TZ: ${HOST_TZ}
      BUNDLE_PATH: /home/dev/.cache/bundle
      CCIC_SCREENSHOT_DIR: /workspace-${SUFFIX}/tmp/screenshots
```

Three things worth calling out:

**`CLAUDE_CONFIG_DIR` is mandatory, not optional.** Claude Code keeps its OAuth token in
`~/.claude/.credentials.json` but the account record and per-project trust in
`~/.claude.json`, *outside* that directory. A volume on `~/.claude` alone will **not** keep
you signed in through a rebuild — `CLAUDE_CONFIG_DIR` must point at the same path so both
files land in the volume.
([docs](https://code.claude.com/docs/en/devcontainer#persist-authentication-and-settings-across-rebuilds))

**The container is long-lived (`sleep infinity`), not one-shot.** `ccic start` is then a
`docker compose exec`, which is what makes `ccic shell`, `ccic psql` and a second concurrent
session possible. A `docker compose run --rm` design forecloses all three.

**Named volume for `~/.cache`** — bundler, npm, pip and Playwright downloads survive
`ccic build`, turning most rebuilds from minutes into seconds.

---

## 6. Images: shared base + thin project layer

```
ccic-base:<ccic-version>-u<UID>-g<GID>        built once per machine, shared by all projects
  ubuntu:24.04 + apt + user + node + mise + claude-code + playwright + chromium   (~3-4 GB)
        │
        └── ccic-<suffix>:latest              per-project, seconds to build
              COPY mise.toml ; mise install
```

Ten projects then cost one base image plus ten small layers rather than ten × 4 GB. The UID
and GID are baked into the base, so they go in the tag — if they ever change, you get a new
base rather than a silently wrong one.

- `ccic build` — builds the base if absent, then the project layer.
- `ccic destroy` — removes the project image and volumes, **never** the shared base.
- `ccic force-rebuild` — `--no-cache` on the project layer; `--base` to also rebuild the base.
- `ccic prune` — removes base images no project references.

### Base image

> The listings below are abridged. `templates/Dockerfile.base` and
> `templates/Dockerfile.project` are the source of truth and are working files as of
> Phase 0 — they additionally carry the mise shims on `PATH`, the `/opt/ccic/browser`
> scratch directory, the pinned `playwright` wrapper and the `ccic-shot` helper described
> in §11.

```dockerfile
# syntax=docker/dockerfile:1
ARG BASE_IMAGE=ubuntu:24.04
FROM ${BASE_IMAGE}

ARG UID=1000
ARG GID=1000
ARG USERNAME=dev
ARG PLAYWRIGHT_VERSION
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8

# ─ system packages ────────────────────────────────────────────────────────
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential git curl wget unzip ca-certificates gnupg sudo tzdata \
      iptables ipset dnsutils \
      libpq-dev postgresql-client libyaml-dev libvips pkg-config \
      libffi-dev libssl-dev zlib1g-dev \
      fonts-liberation fonts-noto-color-emoji fonts-unifont \
      ripgrep fd-find jq less zsh \
 && rm -rf /var/lib/apt/lists/*

# ─ non-root user matching the host ────────────────────────────────────────
# Two collisions to dodge:
#   ubuntu:24.04 ships a `ubuntu` user already at uid 1000
#   macOS's default gid 20 (staff) is `dialout` in Ubuntu — groupadd would fail
RUN set -eux; \
    if getent passwd ${UID} >/dev/null; then \
      userdel -r "$(getent passwd ${UID} | cut -d: -f1)"; fi; \
    if getent group ${GID} >/dev/null; then \
      GRP="$(getent group ${GID} | cut -d: -f1)"; \
    else groupadd -g ${GID} ${USERNAME}; GRP=${USERNAME}; fi; \
    useradd -m -u ${UID} -g "${GRP}" -s /bin/zsh ${USERNAME}; \
    echo "${USERNAME} ALL=(ALL) NOPASSWD:ALL" >/etc/sudoers.d/${USERNAME}

# ─ node (for claude-code + playwright), then Playwright's own system libs ─
RUN curl -fsSL https://deb.nodesource.com/setup_22.x | bash - \
 && apt-get install -y --no-install-recommends nodejs \
 && npx --yes playwright@${PLAYWRIGHT_VERSION} install-deps chromium \
 && rm -rf /var/lib/apt/lists/*

# ─ claude code + chromium, both ccic-owned, both outside the project ──────
ENV PLAYWRIGHT_BROWSERS_PATH=/opt/ms-playwright
RUN npm install -g @anthropic-ai/claude-code playwright@${PLAYWRIGHT_VERSION} \
 && playwright install chromium

# ─ mise ───────────────────────────────────────────────────────────────────
RUN curl -fsSL https://mise.run | MISE_INSTALL_PATH=/usr/local/bin/mise sh

USER ${USERNAME}
RUN echo 'eval "$(/usr/local/bin/mise activate zsh)"' >> ~/.zshrc
```

### Project layer

```dockerfile
ARG CCIC_BASE
FROM ${CCIC_BASE}
COPY --chown=dev:dev mise.toml /tmp/ccic/mise.toml
RUN cd /tmp/ccic && mise trust --yes && mise install --yes && mise reshim
COPY entrypoint.sh init-firewall.sh /usr/local/bin/
ENTRYPOINT ["/usr/local/bin/ccic-entrypoint"]
```

`ccic build` always writes a `mise.toml` into the build context — the project's if it has
one, a stub otherwise — so the `COPY` can never fail on a project without mise.

### On the apt list

The `libasound2t64` / `libatk1.0-0t64` / … block from `0_initial_plan.md` carried a
*"confirm these again"*. **Delete it and stop maintaining it** —
`playwright install-deps chromium` resolves the correct names for the running distro, which
is precisely the problem the `t64` ABI transition created. Keep the three `fonts-*` packages
explicit, or screenshots render text as boxes.

Additions worth making: `ripgrep` and `fd-find` (Claude Code's search is markedly faster
with them), `jq`, `zsh`, `sudo`, `tzdata`, `iptables`/`ipset`/`dnsutils` for the firewall,
and the `libffi-dev`/`libssl-dev`/`zlib1g-dev` trio that Ruby and Python native gems expect.

### Playwright is ccic's, not the project's

Installed globally at a ccic-pinned version, browsers at `/opt/ms-playwright`, entirely
outside the workspace. It exists so *Claude* can do headless checks — it is not there to run
a project's own test suite, and most projects won't use it at all. If a project does carry
its own Playwright it lives in the isolated `node_modules` and the two never meet.

---

## 7. Egress firewall

On by default, since `--dangerously-skip-permissions` is also on by default. Applied by the
entrypoint as root (hence `NET_ADMIN`/`NET_RAW`), which then drops to `dev`.

Default-deny outbound, with an allowlist covering: `api.anthropic.com`,
`console.anthropic.com`, `claude.ai`, `statsig.anthropic.com`; `registry.npmjs.org`,
`rubygems.org`, `pypi.org`, `files.pythonhosted.org`, `index.crates.io`; `github.com`,
`*.githubusercontent.com`, `mise.jdx.dev` (mise pulls toolchains from GitHub releases); and
the `internal` network so postgres stays reachable.

**One improvement over Anthropic's reference script:** it allows UDP/53 to any destination,
so you can point straight at your own nameserver and tunnel out
([issue #36907](https://github.com/anthropics/claude-code/issues/36907)). Restricting DNS to
Docker's embedded resolver on `127.0.0.11` blocks that. It **narrows** the channel rather
than closing it — the embedded resolver still forwards queries upstream, so tunnelling
through it remains possible from inside the container. Treat the firewall as a speed bump
against accidents and casual exfiltration, not a boundary against a hostile repo.

A second honest limit: the allowlist is built from DNS A records captured at apply time.
CDN addresses rotate, so a long-running container can lose access to an allowed domain;
re-running `ccic firewall on` refreshes it.

### The research-session escape hatch

```
ccic firewall off      # flush the rules in the running container — instant, no restart
ccic firewall on
ccic firewall status
ccic start --no-firewall
```

Because the rules are applied by `iptables` inside a long-lived container, toggling is a
`docker exec` away rather than a rebuild.

**Worth knowing about what the firewall actually costs you:** `WebSearch` is executed
server-side by Anthropic, so it keeps working behind the allowlist. `WebFetch` resolves from
inside the container, so it fails on any domain not on the list. That asymmetry is exactly
the research-session friction you described — *verify it in Phase 0 before writing the docs
around it*, since it determines whether `firewall off` is a rare thing or a daily thing.

---

## 8. Config schema — `.ccic.conf`

TOML, matching `mise.toml`'s format. Committable but not required.

```toml
version = 1
suffix  = "acme"
workspace = "/workspace-acme"          # derived from suffix; overridable

[image]
base = "ubuntu:24.04"
apt_packages = []                      # extras on top of the baseline

[postgres]
enabled  = true
version  = "18"                        # 16 | 17 | 18
database = "acme_development"

[redis]
enabled = false                        # second known service; not a plugin system
version = "8"

[isolation]
paths = ["node_modules"]               # container-only overlays — see §4
screenshots = "tmp/screenshots"        # stays on the bind mount, host-visible

[network]
publish = []                           # deliberately empty; escape hatch only

[firewall]
enabled = true
allow = []                             # extra domains

[git]
identity  = true                       # forward user.name / user.email from host
allow_push = false                     # inject no credentials — see below

[env]
passthrough = ["TZ"]                   # forwarded from host env
file = ".ccic.local.env"               # gitignored, injected wholesale

[claude]
skip_permissions = true                # --dangerously-skip-permissions
extra_args = []
```

`version` is there from day one so a future ccic can migrate old files rather than choke.

### Services: two known, not a plugin system

Postgres and Redis as explicit blocks. No generic service mechanism — the compose
generation is a `switch` over two cases, and a plugin architecture for a personal tool is
the definition of going overboard. If a third service ever earns its place, it's another
`case`.

### What goes where

| Path | Committed? | Notes |
|---|---|---|
| `.ccic.conf` | yes | source of truth |
| `.ccic.md` | yes | generated Claude-facing docs, imported by `CLAUDE.md` |
| `.ccic/` | **no** | generated build context; `ccic build` recreates it |
| `.ccic.local.env` | **no** | secrets |
| `tmp/screenshots/` | **no** | Claude's visual output |

`ccic init` appends the gitignore lines itself.

### Git: commit locally, never push

Three layers, strongest first:

1. **Withhold credentials.** No SSH agent forwarding, no `~/.ssh` mount, no `GH_TOKEN`.
   `git push` simply fails to authenticate. This is the default and the primary mechanism —
   the Claude Code docs specifically warn against mounting host secrets into a container
   running arbitrary code.
2. **The firewall.** SSH egress on :22 isn't on the allowlist, so push over SSH dies at the
   network layer too.
~~3. A `pre-push` hook via `core.hooksPath` for a clearer error message.~~ **Tried in Phase 0
   and removed.** Git lists the remote's refs — and therefore authenticates — *before* it
   invokes `pre-push`, so with no credentials the hook never runs and you get the auth error
   anyway. It only fires in the case where pushing would have succeeded. Not worth a global
   `core.hooksPath` that would break husky / lefthook / overcommit. `.ccic.md` tells Claude
   that push is disabled by design, which is the part that actually matters.

Identity comes from the host's `git config user.name` / `user.email`, forwarded as
`GIT_AUTHOR_*` / `GIT_COMMITTER_*`. Also set
`git config --global --add safe.directory /workspace-acme`, or the UID mismatch produces
"dubious ownership" errors on every command.

### UID/GID

Your current `ARG HOST_UID=501` assumes the first user on a Mac. Replace it with detection:
ccic runs `id -u` / `id -g` at build time and passes both as build args. On macOS VirtioFS
remaps ownership anyway so it's cosmetic; on Linux it's the difference between working and
root-owned files everywhere. Passing it is correct on both. The two collisions it has to
survive are handled in the base Dockerfile above.

---

## 9. `.ccic.md` and the CLAUDE.md question

Don't write to the project's `CLAUDE.md` — it's yours, and regenerating would clobber it.
Instead:

1. Generate **`.ccic.md`** at the project root, rendered from the resolved config.
2. Append one import line to `CLAUDE.md`, creating it if absent:
   ```markdown
   @.ccic.md
   ```
   Claude Code resolves `@path` imports, so the content loads without ccic ever owning the
   host file.
3. `ccic build` regenerates `.ccic.md`; the import line never changes.

`.ccic.md` should state what Claude cannot otherwise discover:

- **You are in a container, and it is yours alone.** The host runs its own copy of this
  project; you never share a port, a database, or a `node_modules` with it.
- The workspace is a **bind mount of the host repo** — edits are immediately real.
- **Screenshots go in `tmp/screenshots/`.** This is the channel for showing the user
  anything visual: it's on the bind mount, so they open it directly on the host. Name files
  descriptively and reference them by host-relative path.
- Postgres is at host `db`:5432 with `DATABASE_URL` already exported. Not reachable from
  the host machine — use `psql "$DATABASE_URL"` in here.
- Playwright + Chromium are installed globally with fonts and emoji present. Start the
  app yourself in here if you need to drive it.
- `node_modules` (and any other isolated path) is **container-only**. Running
  `npm install` here is safe and does not touch the host's copy.
- What is **not** available: no published ports, no host network, no Docker socket, no
  `~/.ssh`, no push credentials, no access to other projects. Commit freely; you cannot push.
- Whether the egress firewall is on, and that `WebFetch` to non-allowlisted domains will
  fail while `WebSearch` works.

---

## 10. Commands

| Command | Behaviour |
|---|---|
| `ccic init` | prompt → `.ccic.conf`, `.ccic.md`, gitignore lines, `CLAUDE.md` import. `--non-interactive` with a flag per prompt, so setup is scriptable and testable in CI |
| `ccic build` | render `.ccic/`, build base if absent, build project layer |
| `ccic start` | `compose up -d` → wait for db healthy → `exec -it claude` → `claude --dangerously-skip-permissions`. Fails loudly if `init`/`build` haven't run. Trailing args pass through: `ccic start -- --resume` |
| `ccic shell` | interactive `zsh` in the container |
| `ccic exec <cmd>` | one-off command |
| `ccic psql` | `psql "$DATABASE_URL"` |
| `ccic stop` | `compose stop` |
| `ccic logs [svc]` | `compose logs -f` |
| `ccic status` | containers, image ages, mise tool versions, firewall state, auth state |
| `ccic doctor` | docker running? config parses? base image present? mise tools resolve? db healthy? Claude authenticated? |
| `ccic firewall on\|off\|status` | live toggle via `docker exec` |
| `ccic regen` | re-render `.ccic/` and `.ccic.md` after editing `.ccic.conf`, no rebuild |
| `ccic destroy` | confirm → `compose down -v --rmi local`. Drops the postgres volume, so `--yes` for scripting and a `pg_dump` reminder in the prompt |
| `ccic force-rebuild` | `down` (**without** `-v`) → build `--no-cache` → `up`. Preserves the Claude login, the database and the caches: force-rebuild is about the image, not the data. `--base` to rebuild the base too, `--volumes` to also wipe data |
| `ccic prune` | remove unreferenced base images |
| `ccic upgrade` / `version` | self-update from the GitHub releases API |

No implicit behaviour: `start` never silently runs `init` or `build`, per the stated
principle of controlling rebuilds manually.

---

## 11. Phase 0 results

Phase 0 is **complete**. The container design is proven end-to-end against a fixture
project on this machine (uid 501, gid 20, arm64, Docker Desktop 29.4). Assets live in
`templates/`, driven by `hack/ccic.sh` — a throwaway bash driver that doubles as the
executable spec for the Go CLI.

### Acceptance

| Check | Result |
|---|---|
| `claude` runs in the container | 2.1.260 |
| login survives a `--no-cache` rebuild | yes |
| project node inside the workspace | v22.23.2 |
| ccic's own node outside it | v24.20.0 |
| postgres from the container | `acme_development` |
| postgres from the host | unreachable |
| host `node_modules` | `darwin-arm64` |
| container `node_modules` | `linux-arm64` |
| `git commit` | works, host identity forwarded |
| `git push` | fails (no credentials) |
| firewall allowlist / everything else | 200 / blocked |
| screenshot → host, text and emoji legible | yes |
| host file ownership after container write | `richard` |
| published ports | 0 |

### Seven things the design got wrong

Each of these would have been a confusing bug discovered later, in a real project.

1. **postgres 18 moved its volume mount.** The image now owns the layout below
   `/var/lib/postgresql` and puts data in a major-version subdirectory so
   `pg_upgrade --link` can work; mounting the old `/var/lib/postgresql/data` makes 18+
   refuse to initialise. The mount path is now version-dependent — `<= 17` keeps the old
   path. ([docker-library/postgres#1259](https://github.com/docker-library/postgres/pull/1259))

2. **mise silently ignored the project's toolchain.** The workspace `mise.toml` is untrusted
   at runtime, so mise refused to activate it and `node` quietly fell back to ccic's v24
   instead of the project's v22 — no error, just the wrong version.
   `MISE_TRUSTED_CONFIG_PATHS` fixes it.

3. **`mise activate` doesn't apply to `docker exec`.** It only runs for *interactive* shells,
   so `zsh -lc 'node --version'` never sourced it. mise **shims** on `PATH` work in every
   context and fall through to ccic's node when a project declares none.

4. **Globally-installed Playwright is not importable.** `import { chromium } from 'playwright'`
   fails from any ESM script regardless of `NODE_PATH`, which ESM ignores — so scripted
   screenshots would have been dead on arrival. Fixed with `/opt/ccic/browser/`, a scratch
   directory with Playwright symlinked into its `node_modules`, plus a `ccic-shot` helper
   for the common URL→PNG case. Neither touches the project.

5. **`playwright` is a node script, so the shims could hijack it** with a project's pinned
   node. Now wrapped to run under `/usr/bin/node` explicitly. `claude` turned out to be a
   native binary and is immune.

6. **`force-rebuild` would have deleted your login and your database** — it inherited
   `down -v` from `destroy`. It now preserves volumes.

7. **The `pre-push` hook doesn't work** (see §8) and has been removed.

Two smaller ones: `[ cond ] && ...` as a function's last statement returns 1 and kills the
script under `set -e`; and the uid/gid guards were verified against all three real cases
(1000/1000 deletes Ubuntu's stock user, 501/20 reuses `dialout`, 1000/20 does both).

### Confirmed as designed

- `ipset` + `-m set` work on Ubuntu 24.04's nf_tables backend under Docker Desktop — no
  legacy-iptables fallback needed.
- Extracting only `[tools]` from `mise.toml` is necessary and sufficient: it was tested
  against four of your real project files, and correctly drops `[env] _.source`,
  `_.path` and `[tasks]`.
- Live firewall toggling works via `docker exec` with no container restart.
- The shared base image is worth it: this machine already carries **7 near-identical
  per-project Claude images totalling ~11.2 GB**.

### Still unverified

**`WebFetch` vs `WebSearch` behind the firewall.** Testing it needs an authenticated Claude
session, which the fixture cannot have. The expectation — `WebSearch` unaffected because it
runs server-side, `WebFetch` blocked for non-allowlisted domains — is stated in `.ccic.md`
and should be confirmed on the first real session before that wording is trusted.

---

## 12. Delivery phases

### Phase 0 — prove the containers by hand, no CLI ✅ **done — see §11**
Built as `templates/{Dockerfile.base,Dockerfile.project,compose*.yml,entrypoint.sh,init-firewall.sh,ccic.md.tmpl}`
and driven by `hack/ccic.sh`. Every item below passes against the fixture:

- `claude` runs, logs in once, and *stays* logged in through a full rebuild;
- `psql "$DATABASE_URL"` works inside and fails from the host;
- a Playwright screenshot lands in `tmp/screenshots/` and opens on the host with readable text;
- `npm install` inside leaves the host's `node_modules` untouched, and vice versa;
- host file ownership is correct after Claude writes a file;
- the firewall blocks an arbitrary domain, `ccic firewall off` unblocks it instantly, and
  **you have confirmed what it does to `WebFetch` vs `WebSearch`**;
- `git commit` works, `git push` fails clearly.

All pass. Worth repeating against a real Rails app before Phase 2 — native gems, libvips
and a JS bundler together are a harsher test than the fixture, and `BUNDLE_PATH`
redirection is the one isolation path not yet exercised end to end.

### Phase 1 — CLI skeleton
`init`, `build`, `start`, `destroy`, `force-rebuild`, plus `shell` — templates embedded in
the binary.

### Phase 2 — the rest of the commands
`exec`, `psql`, `status`, `stop`, `logs`, `doctor`, `firewall`, `regen`, `prune`.

### Phase 3 — release pipeline
GoReleaser + a tag-triggered workflow: darwin/linux × arm64/amd64, checksums, GitHub
Release, Homebrew tap. Then `ccic upgrade` / `ccic version`. Note macOS Gatekeeper will
quarantine an unsigned download — notarise, ship via the tap (which sidesteps it), or
document `xattr -d com.apple.quarantine`.

---

## 13. Risks and sharp edges

| Risk | Mitigation |
|---|---|
| Host and container clobbering each other's native builds | §4 — env redirection plus named-volume overlays. The single most important thing to get right |
| Bind-mount I/O on macOS for big trees | VirtioFS + `:delegated`; the `node_modules` overlay already removes the worst offender |
| `ubuntu:24.04` occupies uid 1000; macOS gid 20 is `dialout` | `getent` guards in the base Dockerfile — silent, confusing failures otherwise |
| Firewall blocks something Claude needs mid-session | `ccic firewall off` is instant; `[firewall] allow` for permanent additions |
| Allowlisted CDN rotates IPs mid-session | re-run `ccic firewall on` to re-resolve |
| DNS tunnelling bypasses an IP allowlist | restrict UDP/53 to `127.0.0.11` — narrows it, does not close it (the embedded resolver still forwards upstream) |
| `--dangerously-skip-permissions` writes to your real files | the bind mount *is* the repo — say so plainly in `.ccic.md` and at `init` |
| Credentials sit in a volume Claude can read | container is not a security boundary against a malicious repo; trusted projects only |
| `destroy` drops the postgres volume | confirm by default, `--yes` to skip, `pg_dump` reminder in the prompt |
| Shared base deleted out from under another project | `destroy` never touches the base; only `prune` does, and only when unreferenced |
| Unsigned macOS binary quarantined | notarise, Homebrew tap, or document `xattr -d` |

---

## 14. Decisions taken

| # | Decision |
|---|---|
| 1 | **No published ports.** Host runs the dev environment; the container is Claude's. Screenshots in `tmp/screenshots/` are the channel back. `[network] publish` remains as an escape hatch |
| 2 | `ccic shell` in Phase 1 |
| 3 | Env/secret passthrough via `.ccic.local.env` + host-env allowlist |
| 4 | UID/GID **detected**, not hardcoded — replaces `ARG HOST_UID=501` |
| 5 | Git identity forwarded; **commits yes, pushes no**, enforced by withholding credentials |
| 6 | `ccic doctor` |
| 7 | Postgres + Redis as two known services. No plugin system |
| 8 | `--dangerously-skip-permissions` **on** by default, paired with the egress firewall **on** by default, with `ccic firewall off` for research sessions |
| 9 | **No dotfiles mount.** The container is Claude's; keep it minimal |
| 10 | Shared base image pulled forward from Phase 4 into the core design |
| 11 | `TZ` passthrough |
| 12 | `--non-interactive` init |
| — | Playwright is ccic-pinned and ccic-owned, decoupled from the project entirely |
| — | `ccic-shot` + `/opt/ccic/browser/` give Claude a browser workspace outside the project |

| — | **Language: Go** |

### Still open

- **Postgres default version** — 18 works and is the default. Track latest dynamically, or
  pin and bump with ccic releases?
- **`WebFetch` behind the firewall** — needs one real session to confirm (§11).

---

## Sources

- [Development containers — Claude Code Docs](https://code.claude.com/docs/en/devcontainer)
- [Anthropic reference devcontainer](https://github.com/anthropics/claude-code/tree/main/.devcontainer)
- [Devcontainer firewall DNS gap — anthropics/claude-code#36907](https://github.com/anthropics/claude-code/issues/36907)
- [Bun — Single-file executable](https://bun.com/docs/bundler/executables)
- [Playwright — Browsers & system dependencies](https://playwright.dev/docs/browsers)
- [mise + Docker cookbook](https://mise.jdx.dev/mise-cookbook/docker.html)
