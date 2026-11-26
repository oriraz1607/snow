#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PLUGIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
OUTPUT_DIR="${OUTPUT_DIR:-$PLUGIN_DIR/release}"
GO_BIN="${GO:-go}"

PKG_VERSION="$(node -e "console.log(require('$PLUGIN_DIR/package.json').version)")"
PLUGIN_VERSION="$(node -e "console.log(require('$PLUGIN_DIR/src/plugin.json').info.version)")"
PLUGIN_ID="$(node -e "console.log(require('$PLUGIN_DIR/src/plugin.json').id)")"
EXECUTABLE="$(node -e "console.log(require('$PLUGIN_DIR/src/plugin.json').executable)")"

if [ "$PKG_VERSION" != "$PLUGIN_VERSION" ]; then
  echo "error: version mismatch between package.json ($PKG_VERSION) and src/plugin.json ($PLUGIN_VERSION)" >&2
  exit 1
fi

echo "Building frontend for $PLUGIN_ID v$PLUGIN_VERSION..."
(
  cd "$PLUGIN_DIR"
  npm run build
)

echo "Building multi-arch Go plugin binaries (linux/amd64 and linux/arm64)..."
(
  cd "$PLUGIN_DIR"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 "$GO_BIN" build -trimpath -ldflags="-s -w" -o "dist/${EXECUTABLE}_linux_amd64" ./pkg
  CGO_ENABLED=0 GOOS=linux GOARCH=arm64 "$GO_BIN" build -trimpath -ldflags="-s -w" -o "dist/${EXECUTABLE}_linux_arm64" ./pkg
)
chmod 755 "$PLUGIN_DIR/dist/${EXECUTABLE}_linux_amd64" "$PLUGIN_DIR/dist/${EXECUTABLE}_linux_arm64"

if [ -n "${GRAFANA_ACCESS_POLICY_TOKEN:-}" ]; then
  echo "Signing plugin bundle with @grafana/sign-plugin..."
  SIGN_ARGS=(--distDir "$PLUGIN_DIR/dist")
  if [ -n "${GRAFANA_PLUGIN_ROOT_URLS:-}" ]; then
    SIGN_ARGS+=(--rootUrls "$GRAFANA_PLUGIN_ROOT_URLS")
  fi
  (
    cd "$PLUGIN_DIR"
    npx -y @grafana/sign-plugin@latest "${SIGN_ARGS[@]}"
  )
else
  echo "GRAFANA_ACCESS_POLICY_TOKEN not set; skipping plugin signature step (unsigned artifact)."
fi

mkdir -p "$OUTPUT_DIR"
ARCHIVE_NAME="${PLUGIN_ID}-${PLUGIN_VERSION}.zip"
ARCHIVE_PATH="${OUTPUT_DIR}/${ARCHIVE_NAME}"
STAGE_DIR="$(mktemp -d)"
trap 'rm -rf "$STAGE_DIR"' EXIT

cp -R "$PLUGIN_DIR/dist" "$STAGE_DIR/$PLUGIN_ID"
(
  cd "$STAGE_DIR"
  if command -v zip >/dev/null 2>&1; then
    zip -rq "$ARCHIVE_PATH" "$PLUGIN_ID"
  else
    python3 -m zipfile -c "$ARCHIVE_PATH" "$PLUGIN_ID"
  fi
)

(
  cd "$OUTPUT_DIR"
  sha256sum "$ARCHIVE_NAME" > "${ARCHIVE_NAME}.sha256"
)

echo "Packaged plugin artifact: $ARCHIVE_PATH"
