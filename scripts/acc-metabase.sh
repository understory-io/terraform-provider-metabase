#!/usr/bin/env bash
#
# Start a throwaway Metabase in docker for the acceptance tests, complete its
# setup wizard and mint an admin API key, then print the two environment
# variables the tests read:
#
#   eval "$(scripts/acc-metabase.sh)"
#   mise run test:acc
#
# The image is the OSS edition at the version Understory runs. That covers every
# resource in this provider; group managers and sandboxing are Enterprise-only
# and are not exercised here.
#
# Re-running reuses a container that is already up. `docker rm -f mb-tfacc`
# throws it away.

set -euo pipefail

IMAGE="${METABASE_IMAGE:-metabase/metabase:v0.64.0.4-beta}"
NAME="${METABASE_CONTAINER:-mb-tfacc}"
PORT="${METABASE_PORT:-3999}"
URL="http://localhost:$PORT"
EMAIL="tfacc@example.com"
PASSWORD="tfacc-$(echo "$NAME" | sha256sum | cut -c1-16)"

log() { printf '%s\n' "$*" >&2; }

if ! docker ps --format '{{.Names}}' | grep -qx "$NAME"; then
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  log "starting $IMAGE as $NAME on :$PORT"
  docker run -d --name "$NAME" -p "$PORT:3000" "$IMAGE" >/dev/null
fi

log "waiting for $URL/api/health"
for _ in $(seq 1 120); do
  [ "$(curl -s "$URL/api/health" || true)" = '{"status":"ok"}' ] && break
  sleep 2
done
[ "$(curl -s "$URL/api/health" || true)" = '{"status":"ok"}' ] || { log "metabase did not become healthy"; exit 1; }

# The setup token is only offered until setup completes; after that we log in.
TOKEN=$(curl -s "$URL/api/session/properties" | jq -r '."setup-token" // empty')
if [ -n "$TOKEN" ] && [ "$(curl -s "$URL/api/session/properties" | jq -r '."has-user-setup"')" != "true" ]; then
  log "completing setup"
  SESSION=$(jq -n --arg t "$TOKEN" --arg e "$EMAIL" --arg p "$PASSWORD" '{
      token: $t,
      user: {email: $e, password: $p, first_name: "Terraform", last_name: "Acceptance", site_name: "tfacc"},
      prefs: {site_name: "tfacc", site_locale: "en", allow_tracking: false}
    }' | curl -sf -X POST -H 'Content-Type: application/json' --data @- "$URL/api/setup" | jq -r '.id')
else
  SESSION=$(jq -n --arg e "$EMAIL" --arg p "$PASSWORD" '{username: $e, password: $p}' \
    | curl -sf -X POST -H 'Content-Type: application/json' --data @- "$URL/api/session" | jq -r '.id')
fi

# A fresh key every run: Metabase only shows a key's secret once.
KEY=$(jq -n --arg n "tfacc-$(date +%s%N)" '{name: $n, group_id: 2}' \
  | curl -sf -X POST -H 'Content-Type: application/json' -H "X-Metabase-Session: $SESSION" --data @- "$URL/api/api-key" \
  | jq -r '.unmasked_key')

printf 'export METABASE_URL=%q\n' "$URL"
printf 'export METABASE_API_KEY=%q\n' "$KEY"
