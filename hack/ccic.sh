#!/usr/bin/env bash
#
# Phase 0 hand-driver for ccic.
#
# This is scaffolding, NOT the product. It exists so the container design can be
# proven end-to-end before any Go is written, and it doubles as the executable
# spec for the Phase 1 CLI: every command here maps to a `ccic` subcommand.
#
# Config comes from a flat `.ccic.env` in the project directory. The real tool
# reads `.ccic.conf` (TOML) — parsing TOML in bash is not a good use of anyone's
# time, and the container design does not depend on it.
#
#   hack/ccic.sh init        [dir]   write a starter .ccic.env
#   hack/ccic.sh build-base          build the shared base image
#   hack/ccic.sh build       [dir]   render build context + build project layer
#   hack/ccic.sh up          [dir]   start containers
#   hack/ccic.sh start       [dir]   up, then run claude
#   hack/ccic.sh shell       [dir]   interactive zsh
#   hack/ccic.sh exec  <dir> <cmd…>  one-off command
#   hack/ccic.sh psql        [dir]
#   hack/ccic.sh firewall <on|off|status> [dir]
#   hack/ccic.sh status      [dir]
#   hack/ccic.sh stop        [dir]
#   hack/ccic.sh destroy     [dir]
#   hack/ccic.sh force-rebuild [dir]

set -euo pipefail

CCIC_VERSION="0.1.0"
REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TEMPLATES="${REPO_ROOT}/templates"

die()  { printf '\033[31mccic: %s\033[0m\n' "$*" >&2; exit 1; }
info() { printf '\033[36mccic:\033[0m %s\n' "$*" >&2; }
ok()   { printf '\033[32mccic:\033[0m %s\n' "$*" >&2; }

# --------------------------------------------------------------------------
# config
# --------------------------------------------------------------------------
load_config() {
  PROJECT_DIR="$(cd "${1:-$PWD}" && pwd)"
  [ -f "${PROJECT_DIR}/.ccic.env" ] || die "no .ccic.env in ${PROJECT_DIR} — run: hack/ccic.sh init ${PROJECT_DIR}"

  # shellcheck disable=SC1091
  set -a; . "${PROJECT_DIR}/.ccic.env"; set +a

  : "${CCIC_SUFFIX:?CCIC_SUFFIX must be set in .ccic.env}"

  HOST_UID="$(id -u)"
  HOST_GID="$(id -g)"
  CCIC_USER="${CCIC_USER:-dev}"
  CCIC_WORKSPACE="${CCIC_WORKSPACE:-/workspace-${CCIC_SUFFIX}}"
  CCIC_HOST_DIR="${PROJECT_DIR}"
  CCIC_BASE_IMAGE="ccic-base:${CCIC_VERSION}-u${HOST_UID}-g${HOST_GID}"
  CCIC_IMAGE="ccic-${CCIC_SUFFIX}:latest"
  CCIC_PG_DATABASE="${CCIC_PG_DATABASE:-${CCIC_SUFFIX}_development}"
  # postgres 18 moved the expected mount point up one level; see compose.postgres.yml
  if [ "${CCIC_PG_VERSION:-18}" -ge 18 ] 2>/dev/null; then
    CCIC_PG_DATA_MOUNT="/var/lib/postgresql"
  else
    CCIC_PG_DATA_MOUNT="/var/lib/postgresql/data"
  fi
  CCIC_TZ="${CCIC_TZ:-$(readlink /etc/localtime | sed 's|.*/zoneinfo/||')}"
  CTX="${PROJECT_DIR}/.ccic"
  PROJECT_NAME="ccic-${CCIC_SUFFIX}"

  # NB: `[ ... ] && ...` as the last statement of a function returns 1 when the
  # test is false, which under `set -e` kills the caller. Use if-blocks.
  COMPOSE_FILES=(-f "${CTX}/compose.yml")
  if [ "${CCIC_POSTGRES:-1}" = "1" ]; then
    COMPOSE_FILES+=(-f "${CTX}/compose.postgres.yml")
  fi
  if [ "${CCIC_REDIS:-0}" = "1" ]; then
    COMPOSE_FILES+=(-f "${CTX}/compose.redis.yml")
  fi
  return 0
}

dc() { docker compose --project-directory "${CTX}" "${COMPOSE_FILES[@]}" -p "${PROJECT_NAME}" "$@"; }

# Run as the unprivileged user: Claude Code refuses --dangerously-skip-permissions
# when launched as root, and the container's PID 1 is root so it can manage iptables.
dexec() { dc exec -u "${CCIC_USER}" "$@"; }

# --------------------------------------------------------------------------
# render build context
# --------------------------------------------------------------------------
extract_mise_tools() {
  # Only the [tools] table. A real project mise.toml carries [env] directives
  # such as `_.source = ".env.dev"` pointing at files that are not in the build
  # context, which would make `mise install` fail.
  local src="${PROJECT_DIR}/mise.toml" dst="${CTX}/mise.toml"
  if [ -f "${src}" ]; then
    awk '
      /^\[tools\]/       { in_tools=1; print; next }
      /^\[/ && in_tools  { in_tools=0 }
      in_tools           { print }
    ' "${src}" > "${dst}"
    if ! grep -q '^\[tools\]' "${dst}"; then echo '[tools]' > "${dst}"; fi
    info "mise tools: $(grep -cve '^\s*$' -e '^\[' "${dst}") entry(ies) from mise.toml"
  else
    echo '[tools]' > "${dst}"
    info "no mise.toml — project layer installs no extra toolchains"
  fi
}

render_ccic_md() {
  # Phase 0 uses python3 for the substitution; the Go tool will use
  # text/template. Only the rendered output shape matters here.
  CCIC_TMPL="${TEMPLATES}/ccic.md.tmpl" \
  CCIC_OUT="${PROJECT_DIR}/.ccic.md" \
  CCIC_TOOLS_FILE="${CTX}/mise.toml" \
  P_SUFFIX="${CCIC_SUFFIX}" \
  P_WORKSPACE="${CCIC_WORKSPACE}" \
  P_SHOTS="${CCIC_SCREENSHOTS:-tmp/screenshots}" \
  P_NODE="${CCIC_NODE_MAJOR:-24}" \
  P_PG="${CCIC_POSTGRES:-1}" \
  P_PGDB="${CCIC_PG_DATABASE}" \
  P_PGVER="${CCIC_PG_VERSION:-18}" \
  P_REDIS="${CCIC_REDIS:-0}" \
  P_FW="${CCIC_FIREWALL:-1}" \
  P_ISO="node_modules" \
  python3 - <<'PYEOF'
import os, re

tools = []
try:
    for line in open(os.environ["CCIC_TOOLS_FILE"]):
        line = line.strip()
        if line and not line.startswith("[") and "=" in line:
            tools.append(line.replace('"', "").replace(" ", ""))
except FileNotFoundError:
    pass
tools_str = ", ".join("`%s`" % t for t in tools) if tools else "_none declared_"

if os.environ["P_PG"] == "1":
    db = (
        "PostgreSQL {ver} is running in a second container on host `db`, port 5432.\n"
        "`DATABASE_URL`, `PGHOST`, `PGUSER`, `PGPASSWORD` and `PGDATABASE` are already\n"
        "exported, so `psql` with no arguments connects to `{db}`.\n\n"
        "This database is reachable from this container and from nowhere else — not the\n"
        "host machine, not the internet. It is **not** the human's development database,\n"
        "so migrate, seed, reset and drop it freely. Schema files you change (`schema.rb`,\n"
        "migration files) are on the bind mount and do reach them."
    ).format(ver=os.environ["P_PGVER"], db=os.environ["P_PGDB"])
else:
    db = "No database container is configured for this project."

if os.environ["P_REDIS"] == "1":
    db += "\n\nRedis is available on host `redis`, port 6379, via `REDIS_URL`."

if os.environ["P_FW"] == "1":
    fw = (
        "An egress firewall is **on**. Outbound traffic is restricted to an allowlist:\n"
        "the Anthropic API, GitHub, and the npm/RubyGems/PyPI/crates registries.\n\n"
        "The practical consequence: `WebFetch` against a domain outside that list will\n"
        "fail, while `WebSearch` is unaffected. If you need a blocked domain, say so —\n"
        "the human can run `ccic firewall off` for the session, or add it permanently."
    )
else:
    fw = "The egress firewall is **off** for this project. Outbound traffic is unrestricted."

iso = "\n".join("- `%s/`" % p for p in os.environ["P_ISO"].split())

out = open(os.environ["CCIC_TMPL"]).read()
for k, v in {
    "SUFFIX": os.environ["P_SUFFIX"],
    "WORKSPACE": os.environ["P_WORKSPACE"],
    "SCREENSHOTS": os.environ["P_SHOTS"],
    "NODE_MAJOR": os.environ["P_NODE"],
    "MISE_TOOLS": tools_str,
    "DATABASE_SECTION": db,
    "ISOLATED_PATHS": iso,
    "FIREWALL_SECTION": fw,
}.items():
    out = out.replace("{{%s}}" % k, v)
open(os.environ["CCIC_OUT"], "w").write(out)
PYEOF

  # Non-destructive: ccic never owns CLAUDE.md, it only adds one import line.
  local claude_md="${PROJECT_DIR}/CLAUDE.md"
  if ! grep -qxF "@.ccic.md" "${claude_md}" 2>/dev/null; then
    { [ -s "${claude_md}" ] && echo; echo "@.ccic.md"; } >> "${claude_md}"
    info "added @.ccic.md import to CLAUDE.md"
  fi
  ok "rendered ${PROJECT_DIR}/.ccic.md"
}

render() {
  mkdir -p "${CTX}"
  cp "${TEMPLATES}/Dockerfile.project" \
     "${TEMPLATES}/compose.yml" \
     "${TEMPLATES}/compose.postgres.yml" \
     "${TEMPLATES}/compose.redis.yml" \
     "${TEMPLATES}/entrypoint.sh" \
     "${TEMPLATES}/init-firewall.sh" \
     "${CTX}/"
  extract_mise_tools

  cat > "${CTX}/.env" <<ENVEOF
CCIC_SUFFIX=${CCIC_SUFFIX}
CCIC_USER=${CCIC_USER}
CCIC_WORKSPACE=${CCIC_WORKSPACE}
CCIC_HOST_DIR=${CCIC_HOST_DIR}
CCIC_IMAGE=${CCIC_IMAGE}
CCIC_BASE_IMAGE=${CCIC_BASE_IMAGE}
CCIC_SCREENSHOTS=${CCIC_SCREENSHOTS:-tmp/screenshots}
CCIC_FIREWALL=${CCIC_FIREWALL:-1}
CCIC_FIREWALL_ALLOW=${CCIC_FIREWALL_ALLOW:-}
CCIC_TZ=${CCIC_TZ}
CCIC_PG_VERSION=${CCIC_PG_VERSION:-18}
CCIC_PG_DATABASE=${CCIC_PG_DATABASE}
CCIC_PG_DATA_MOUNT=${CCIC_PG_DATA_MOUNT}
CCIC_REDIS_VERSION=${CCIC_REDIS_VERSION:-8}
GIT_AUTHOR_NAME=$(git -C "${PROJECT_DIR}" config user.name 2>/dev/null || echo "")
GIT_AUTHOR_EMAIL=$(git -C "${PROJECT_DIR}" config user.email 2>/dev/null || echo "")
ENVEOF
  render_ccic_md
  ok "rendered ${CTX}"
}

# --------------------------------------------------------------------------
# commands
# --------------------------------------------------------------------------
cmd_init() {
  local dir; dir="$(cd "${1:-$PWD}" && pwd)"
  [ -f "${dir}/.ccic.env" ] && die ".ccic.env already exists in ${dir}"
  cat > "${dir}/.ccic.env" <<ENVEOF
CCIC_SUFFIX=$(basename "${dir}" | tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-' | sed 's/-*$//')
CCIC_POSTGRES=1
CCIC_PG_VERSION=18
CCIC_REDIS=0
CCIC_FIREWALL=1
CCIC_FIREWALL_ALLOW=
CCIC_SCREENSHOTS=tmp/screenshots
ENVEOF
  for line in ".ccic/" ".ccic.local.env" "tmp/screenshots/"; do
    grep -qxF "${line}" "${dir}/.gitignore" 2>/dev/null || echo "${line}" >> "${dir}/.gitignore"
  done
  ok "wrote ${dir}/.ccic.env"
}

cmd_build_base() {
  local uid gid tag
  uid="$(id -u)"; gid="$(id -g)"
  tag="ccic-base:${CCIC_VERSION}-u${uid}-g${gid}"
  info "building ${tag} (this is the slow one — several minutes, once per machine)"
  docker build \
    -f "${TEMPLATES}/Dockerfile.base" \
    --build-arg "UID=${uid}" \
    --build-arg "GID=${gid}" \
    --build-arg "USERNAME=${CCIC_USER:-dev}" \
    -t "${tag}" \
    "${TEMPLATES}"
  ok "built ${tag}"
}

cmd_build() {
  load_config "${1:-}"
  docker image inspect "${CCIC_BASE_IMAGE}" >/dev/null 2>&1 \
    || die "base image ${CCIC_BASE_IMAGE} missing — run: hack/ccic.sh build-base"
  render
  dc build "${@:2}"
  ok "built ${CCIC_IMAGE}"
}

cmd_up() {
  load_config "${1:-}"
  [ -d "${CTX}" ] || die "no build context — run: hack/ccic.sh build"
  dc up -d
  ok "containers up"
}

cmd_start() {
  load_config "${1:-}"
  docker image inspect "${CCIC_IMAGE}" >/dev/null 2>&1 \
    || die "image ${CCIC_IMAGE} missing — run: hack/ccic.sh build"
  dc up -d
  info "launching claude in ${CCIC_WORKSPACE}"
  dexec claude zsh -lc "claude --dangerously-skip-permissions ${*:2}"
}

cmd_shell()    { load_config "${1:-}"; dexec claude zsh -l; }
cmd_exec()     { load_config "${1:-}"; dexec claude zsh -lc "${*:2}"; }
cmd_psql()     { load_config "${1:-}"; dexec claude psql "\$DATABASE_URL"; }
cmd_stop()     { load_config "${1:-}"; dc stop; ok "stopped"; }
cmd_logs()     { load_config "${1:-}"; dc logs -f "${@:2}"; }

cmd_firewall() {
  local action="${1:-status}"
  load_config "${2:-}"
  dc exec -u root claude /usr/local/bin/ccic-firewall "${action}"
}

cmd_status() {
  load_config "${1:-}"
  echo "suffix       ${CCIC_SUFFIX}"
  echo "workspace    ${CCIC_WORKSPACE}  <-  ${CCIC_HOST_DIR}"
  echo "base image   ${CCIC_BASE_IMAGE}  $(docker image inspect -f '{{.Created}}' "${CCIC_BASE_IMAGE}" 2>/dev/null || echo '(missing)')"
  echo "image        ${CCIC_IMAGE}  $(docker image inspect -f '{{.Created}}' "${CCIC_IMAGE}" 2>/dev/null || echo '(missing)')"
  echo
  dc ps
  echo
  if dc ps --status running --quiet claude >/dev/null 2>&1 && [ -n "$(dc ps --status running --quiet claude)" ]; then
    echo "firewall     $(dc exec -u root claude /usr/local/bin/ccic-firewall status 2>/dev/null || echo unknown)"
    echo "tools        $(dexec claude zsh -lc 'mise ls --installed 2>/dev/null | tr "\n" " "' || true)"
    echo "claude auth  $(dexec claude zsh -lc 'test -f "$CLAUDE_CONFIG_DIR/.credentials.json" && echo "logged in" || echo "not logged in"')"
  fi
}

cmd_destroy() {
  load_config "${1:-}"
  printf 'This removes containers, volumes (INCLUDING the postgres data) and the\nproject image for %s. The shared base image is kept.\nContinue? [y/N] ' "${CCIC_SUFFIX}"
  read -r reply
  [ "${reply}" = "y" ] || die "aborted"
  dc down -v --rmi local --remove-orphans
  rm -rf "${CTX}"
  ok "destroyed"
}

cmd_force_rebuild() {
  load_config "${1:-}"
  # Deliberately NOT `down -v`. force-rebuild is about the image, not the data:
  # wiping volumes here would delete the Claude Code login and the database on
  # every rebuild, which is exactly the friction the claude-config volume exists
  # to remove. Use `destroy` when you actually want the data gone.
  dc down --rmi local --remove-orphans 2>/dev/null || true
  render
  dc build --no-cache
  dc up -d
  ok "rebuilt from scratch (volumes preserved: login and database intact)"
}

case "${1:-}" in
  init)          shift; cmd_init "$@" ;;
  build-base)    shift; cmd_build_base "$@" ;;
  build)         shift; cmd_build "$@" ;;
  up)            shift; cmd_up "$@" ;;
  start)         shift; cmd_start "$@" ;;
  shell)         shift; cmd_shell "$@" ;;
  exec)          shift; cmd_exec "$@" ;;
  psql)          shift; cmd_psql "$@" ;;
  logs)          shift; cmd_logs "$@" ;;
  firewall)      shift; cmd_firewall "$@" ;;
  status)        shift; cmd_status "$@" ;;
  stop)          shift; cmd_stop "$@" ;;
  destroy)       shift; cmd_destroy "$@" ;;
  force-rebuild) shift; cmd_force_rebuild "$@" ;;
  *) sed -n '3,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2 ;;
esac
