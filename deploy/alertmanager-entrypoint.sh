#!/bin/sh
set -eu

if [ -z "${ALERTMANAGER_WEBHOOK_TOKEN:-}" ]; then
  echo "error: ALERTMANAGER_WEBHOOK_TOKEN must be set" >&2
  exit 1
fi

printf '%s' "$ALERTMANAGER_WEBHOOK_TOKEN" > /tmp/webhook_token
chmod 600 /tmp/webhook_token

exec /bin/alertmanager "$@"
