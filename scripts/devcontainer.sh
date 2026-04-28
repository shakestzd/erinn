#!/usr/bin/env bash
# HtmlGraph devcontainer helper — wraps devcontainer CLI and docker for common workflows
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR/.."

SUBCOMMAND="${1:-help}"

need_devcontainer() {
  if ! command -v devcontainer >/dev/null 2>&1; then
    echo "devcontainer CLI not installed — install with: npm install -g @devcontainers/cli" >&2
    exit 1
  fi
}

find_container() {
  docker ps -aq --filter "label=devcontainer.local_folder=$PWD"
}

case "$SUBCOMMAND" in
  up)
    need_devcontainer
    devcontainer up --workspace-folder .
    ;;
  rebuild)
    need_devcontainer
    devcontainer up --workspace-folder . --remove-existing-container
    ;;
  shell)
    need_devcontainer
    devcontainer exec --workspace-folder . \
      --remote-env "TERM=xterm-256color" \
      --remote-env "COLORTERM=${COLORTERM:-truecolor}" \
      zsh
    ;;
  stop)
    CID="$(find_container)"
    if [[ -z "$CID" ]]; then
      echo "no container"
      exit 0
    fi
    docker stop "$CID"
    ;;
  logs)
    CID="$(find_container)"
    if [[ -z "$CID" ]]; then
      echo "no container"
      exit 0
    fi
    docker logs -f "$CID"
    ;;
  status)
    CID="$(find_container)"
    if [[ -z "$CID" ]]; then
      echo "no container"
      exit 0
    fi
    STATE="$(docker inspect --format '{{.State.Status}}' "$CID")"
    echo "$CID  $STATE"
    ;;
  help)
    cat <<'EOF'
Usage: devcontainer.sh <subcommand>

Subcommands:
  up        Create or reuse the devcontainer (cached image)
  rebuild   Full rebuild — removes existing container first
  shell     Open an interactive zsh shell inside the running container
  stop      Stop the running container
  logs      Stream container logs (follow)
  status    Show container ID and state, or "no container" if none
  help      Show this message
EOF
    exit 0
    ;;
  *)
    echo "Unknown subcommand: $SUBCOMMAND" >&2
    exec "$0" help
    exit 2
    ;;
esac
