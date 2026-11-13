#!/usr/bin/env bash
# Build treehouse from this checkout for a running Linux container and install it
# into that container's ~/.local/bin. Optionally set the container's pool root.
#
#   scripts/install-devcontainer.sh <container> [<pool-root>]
#
# Containers lose both on recreate, so re-run after each rebuild.
# CONTAINER_USER overrides the in-container user (default: vscode).
set -euo pipefail

container="${1:?usage: $(basename "$0") <container> [<pool-root>]}"
pool_root="${2:-}"
user="${CONTAINER_USER:-vscode}"
src="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

case "$(docker exec "$container" uname -m)" in
  aarch64 | arm64) arch=arm64 ;;
  x86_64 | amd64) arch=amd64 ;;
  *) echo "unsupported container architecture" >&2; exit 1 ;;
esac
version="v$(sed -n 's/.*": "\([^"]*\)".*/\1/p' "$src/.release-please-manifest.json")"

docker run --rm -e GOOS=linux -e GOARCH="$arch" -e CGO_ENABLED=0 \
  -v "$src":/src -w /src -v treehouse-gomod:/go/pkg/mod -v treehouse-gocache:/root/.cache/go-build \
  golang:1.25 go build -buildvcs=false -ldflags "-X main.version=$version" -o "dist/treehouse-linux-$arch" .

home="$(docker exec -u "$user" "$container" sh -c 'echo "$HOME"')"
docker exec -u "$user" "$container" mkdir -p "$home/.local/bin" "$home/.config/treehouse"
docker cp "$src/dist/treehouse-linux-$arch" "$container:$home/.local/bin/treehouse"
docker exec -u root "$container" chown "$user:" "$home/.local/bin/treehouse"

if [ -n "$pool_root" ]; then
  docker exec -u "$user" -e POOL_ROOT="$pool_root" "$container" sh -c '
    cfg="$HOME/.config/treehouse/config.toml"
    touch "$cfg"
    grep -v "^root *=" "$cfg" > "$cfg.tmp" || true
    printf "root = \"%s\"\n" "$POOL_ROOT" >> "$cfg.tmp"
    mv "$cfg.tmp" "$cfg"'
fi

docker exec -u "$user" "$container" sh -c '"$HOME/.local/bin/treehouse" --version'
