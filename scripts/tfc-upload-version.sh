#!/usr/bin/env bash
#
# Upload a goreleaser-built provider version to the Understory TFC private
# registry.
#
# The GPG key this signs against is the org's existing provider-signing key
# (already uploaded to the `understory` namespace, and shared with the hubspot
# and shiitake providers) - do NOT mint a second one.
#
# Inputs (env):
#   TFC_TOKEN        — TFC user/team token with access to the org
#   GPG_FINGERPRINT  — full GPG fingerprint of the signing key (40 hex chars).
#                      Already uploaded to the org's GPG keys.
#   VERSION          — semver without leading "v" (e.g. 1.0.1).
#                      Defaults to GITHUB_REF_NAME stripped of leading "v".
#
# Reads goreleaser artifacts from ./dist/ (the default output dir).
#
# Sequence (per HashiCorp's TFC private registry API):
#   1. POST a registry-provider-versions resource → returns presigned upload
#      URLs for SHASUMS and SHASUMS.sig.
#   2. PUT the two files.
#   3. For each platform zip: POST a registry-provider-version-platforms
#      resource → returns a presigned URL → PUT the zip.
#
# Idempotent: re-uploading an existing version returns 422 and we skip.

set -euo pipefail

ORG="understory"
REGISTRY="private"
NAMESPACE="understory"
NAME="metabase"
TFC_HOST="${TFC_HOST:-app.terraform.io}"
DIST_DIR="${DIST_DIR:-dist}"

: "${TFC_TOKEN:?TFC_TOKEN must be set}"
: "${GPG_FINGERPRINT:?GPG_FINGERPRINT must be set}"

VERSION="${VERSION:-${GITHUB_REF_NAME:-}}"
VERSION="${VERSION#v}"
[ -n "$VERSION" ] || { echo "VERSION (or GITHUB_REF_NAME) must be set"; exit 1; }

# TFC expects the GPG key id as the trailing 16 hex chars of the fingerprint.
KEY_ID="${GPG_FINGERPRINT: -16}"

SHASUMS_FILE="$DIST_DIR/terraform-provider-${NAME}_${VERSION}_SHA256SUMS"
SHASUMS_SIG_FILE="${SHASUMS_FILE}.sig"

[ -f "$SHASUMS_FILE" ]     || { echo "missing $SHASUMS_FILE"; exit 1; }
[ -f "$SHASUMS_SIG_FILE" ] || { echo "missing $SHASUMS_SIG_FILE — was the build signed?"; exit 1; }

api() {
  local method="$1" path="$2"
  shift 2
  curl -sS -w "\n%{http_code}" \
    --header "Authorization: Bearer $TFC_TOKEN" \
    --header "Content-Type: application/vnd.api+json" \
    --request "$method" \
    "https://$TFC_HOST$path" \
    "$@"
}

# 1. Create version
echo "==> creating version $VERSION (key-id $KEY_ID)"
VERSION_BODY="$(jq -n \
  --arg version "$VERSION" \
  --arg key_id "$KEY_ID" \
  '{
    data: {
      type: "registry-provider-versions",
      attributes: {
        version: $version,
        "key-id": $key_id,
        protocols: ["6.0"]
      }
    }
  }')"

VERSION_RESP="$(api POST "/api/v2/organizations/$ORG/registry-providers/$REGISTRY/$NAMESPACE/$NAME/versions" --data "$VERSION_BODY")"
VERSION_HTTP="$(echo "$VERSION_RESP" | tail -n1)"
VERSION_JSON="$(echo "$VERSION_RESP" | sed '$d')"

if [ "$VERSION_HTTP" = "422" ]; then
  echo "version $VERSION already exists, fetching its links"
  VERSION_RESP="$(api GET "/api/v2/organizations/$ORG/registry-providers/$REGISTRY/$NAMESPACE/$NAME/versions/$VERSION")"
  VERSION_JSON="$(echo "$VERSION_RESP" | sed '$d')"
elif [ "$VERSION_HTTP" != "201" ] && [ "$VERSION_HTTP" != "200" ]; then
  echo "version create failed: $VERSION_HTTP"
  echo "$VERSION_JSON"
  exit 1
fi

SHASUMS_UPLOAD="$(echo "$VERSION_JSON" | jq -r '.data.links."shasums-upload" // empty')"
SHASUMS_SIG_UPLOAD="$(echo "$VERSION_JSON" | jq -r '.data.links."shasums-sig-upload" // empty')"

# 2. Upload shasums + sig
if [ -n "$SHASUMS_UPLOAD" ]; then
  echo "==> uploading SHASUMS"
  curl -sS --request PUT --upload-file "$SHASUMS_FILE" "$SHASUMS_UPLOAD" >/dev/null
fi
if [ -n "$SHASUMS_SIG_UPLOAD" ]; then
  echo "==> uploading SHASUMS.sig"
  curl -sS --request PUT --upload-file "$SHASUMS_SIG_FILE" "$SHASUMS_SIG_UPLOAD" >/dev/null
fi

# 3. Per-platform: register + upload
echo "==> uploading platform binaries"
while IFS= read -r line; do
  shasum="$(echo "$line" | awk '{print $1}')"
  filename="$(echo "$line" | awk '{print $2}' | sed 's|^\*||')"

  # Parse OS and arch out of: terraform-provider-metabase_<ver>_<os>_<arch>.zip
  rest="${filename#terraform-provider-${NAME}_${VERSION}_}"
  rest="${rest%.zip}"
  os="${rest%_*}"
  arch="${rest##*_}"

  case "$filename" in
    *.zip) ;;
    *) continue ;;
  esac

  zip_path="$DIST_DIR/$filename"
  [ -f "$zip_path" ] || { echo "missing $zip_path, skipping"; continue; }

  echo "  - $os/$arch"
  PLATFORM_BODY="$(jq -n \
    --arg os "$os" --arg arch "$arch" \
    --arg shasum "$shasum" --arg filename "$filename" \
    '{
      data: {
        type: "registry-provider-version-platforms",
        attributes: {
          os: $os, arch: $arch,
          shasum: $shasum, filename: $filename
        }
      }
    }')"

  PLATFORM_RESP="$(api POST "/api/v2/organizations/$ORG/registry-providers/$REGISTRY/$NAMESPACE/$NAME/versions/$VERSION/platforms" --data "$PLATFORM_BODY")"
  PLATFORM_HTTP="$(echo "$PLATFORM_RESP" | tail -n1)"
  PLATFORM_JSON="$(echo "$PLATFORM_RESP" | sed '$d')"

  if [ "$PLATFORM_HTTP" = "422" ]; then
    echo "    already exists, skipping"
    continue
  elif [ "$PLATFORM_HTTP" != "201" ] && [ "$PLATFORM_HTTP" != "200" ]; then
    echo "    failed: $PLATFORM_HTTP"
    echo "$PLATFORM_JSON"
    exit 1
  fi

  PROVIDER_BINARY_UPLOAD="$(echo "$PLATFORM_JSON" | jq -r '.data.links."provider-binary-upload"')"
  curl -sS --request PUT --upload-file "$zip_path" "$PROVIDER_BINARY_UPLOAD" >/dev/null
done < "$SHASUMS_FILE"

echo "==> done. version $VERSION uploaded to $TFC_HOST/$ORG/registry/private/providers/$NAMESPACE/$NAME"
