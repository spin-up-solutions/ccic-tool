#!/usr/bin/env bash
#
# ccic container entrypoint. Runs as root, does the setup that can only be done
# with privileges, then drops to the unprivileged user and execs CMD.
#
# Nothing here talks to the host: every input arrives as an environment
# variable from compose.yml.

set -euo pipefail

log() { printf 'ccic: %s\n' "$*" >&2; }

CCIC_USER="${CCIC_USER:-dev}"
CCIC_WORKSPACE="${CCIC_WORKSPACE:-/workspace}"
CCIC_UID="$(id -u "${CCIC_USER}")"
CCIC_GID="$(id -g "${CCIC_USER}")"
CCIC_HOME="$(getent passwd "${CCIC_USER}" | cut -d: -f6)"

# ---------------------------------------------------------------------------
# 1. Named-volume ownership.
#
# Docker creates a named volume's mountpoint root-owned when the path does not
# exist in the image — which is exactly the case for the isolation overlays
# (node_modules and friends). Without this, the first `npm install` fails with
# EACCES. Only the mountpoint itself is chowned, never its contents, so this
# stays O(1) no matter how large the volume grows.
# ---------------------------------------------------------------------------
for path in ${CCIC_CHOWN_PATHS:-}; do
  [ -d "${path}" ] || continue
  owner="$(stat -c %u "${path}")"
  if [ "${owner}" != "${CCIC_UID}" ]; then
    log "claiming ${path} for ${CCIC_USER}"
    chown "${CCIC_UID}:${CCIC_GID}" "${path}"
  fi
done

# ---------------------------------------------------------------------------
# 2. Screenshot drop-box.
#
# This is the channel back to the human: it lives on the bind mount, so files
# written here open directly on the host. Deliberately NOT covered by an
# isolation volume.
# ---------------------------------------------------------------------------
if [ -n "${CCIC_SCREENSHOT_DIR:-}" ]; then
  install -d -o "${CCIC_UID}" -g "${CCIC_GID}" "${CCIC_SCREENSHOT_DIR}"
fi

# ---------------------------------------------------------------------------
# 3. Git: commit locally, never push.
#
# Enforcement is the absence of credentials — no ssh agent, no ~/.ssh, no
# token — so a push cannot authenticate.
#
# A pre-push hook was tried here and removed: git lists the remote's refs (and
# therefore authenticates) BEFORE invoking pre-push, so with no credentials the
# hook never runs and the user sees the auth error regardless. It only ever
# fires in the case where pushing would have worked. Not worth the cost of a
# global core.hooksPath, which overrides project hooks (husky, lefthook,
# overcommit). .ccic.md tells Claude that push is disabled by design.
# ---------------------------------------------------------------------------
GITCONFIG="${CCIC_HOME}/.gitconfig"
{
  echo "# managed by ccic — regenerated on every container start"
  echo "[safe]"
  echo "    directory = ${CCIC_WORKSPACE}"
  echo "    directory = *"
  if [ -n "${GIT_AUTHOR_NAME:-}" ]; then
    echo "[user]"
    echo "    name = ${GIT_AUTHOR_NAME}"
    [ -n "${GIT_AUTHOR_EMAIL:-}" ] && echo "    email = ${GIT_AUTHOR_EMAIL}"
  fi
} > "${GITCONFIG}"
chown "${CCIC_UID}:${CCIC_GID}" "${GITCONFIG}"

# ---------------------------------------------------------------------------
# 4. Egress firewall.
# ---------------------------------------------------------------------------
if [ "${CCIC_FIREWALL:-0}" = "1" ]; then
  if /usr/local/bin/ccic-firewall on; then
    log "egress firewall active"
  else
    log "WARNING: firewall failed to apply — continuing WITHOUT egress restrictions"
  fi
else
  log "egress firewall disabled by configuration"
fi

# ---------------------------------------------------------------------------
# 5. Drop privileges and hand over.
#
# setpriv comes from util-linux, so no gosu/su-exec dependency. exec keeps the
# process at PID 1 semantics under `init: true`, so signals and reaping work.
# ---------------------------------------------------------------------------
# Readiness marker for the compose healthcheck. Setup above takes a few seconds
# (mostly firewall DNS resolution), and without this `up` returns while the
# container is still initialising, so `status` and `doctor` report stale state.
: > /tmp/ccic-ready

log "ready — workspace ${CCIC_WORKSPACE}, user ${CCIC_USER} (${CCIC_UID}:${CCIC_GID})"
exec setpriv --reuid="${CCIC_UID}" --regid="${CCIC_GID}" --init-groups -- "$@"
