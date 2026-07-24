#!/usr/bin/env bash
# Install the latest patched codex binary from denysvitali/codex CI artifacts.
# Usage: ./install-codex.sh
set -euo pipefail

REPO="denysvitali/codex"
GH_PROXY="http://gh-proxy.gh-proxy.svc.cluster.local/api"
GH_USER="workspace-denys"
GH_TOKEN="${GH_TOKEN:-4e_ZhuRxdSJHmJZj7M32cb5qJ3b4SDON17SpXMkgCZo}"
CODEX_DIR="${HOME}/.codex/packages/standalone/releases/0.145.0-x86_64-unknown-linux-musl/bin"
ARTIFACT_NAME="codex-x86_64-unknown-linux-musl"

auth=(-u "${GH_USER}:${GH_TOKEN}")

echo "→ Finding latest successful build on main..."
run_id=$(curl -sf "${auth[@]}" \
  "${GH_PROXY}/repos/${REPO}/actions/runs?branch=main&status=success&per_page=1" \
  | python3 -c "import sys,json; print(json.load(sys.stdin)['workflow_runs'][0]['id'])")
echo "  Run: ${run_id}"

echo "→ Finding artifact '${ARTIFACT_NAME}'..."
artifact_id=$(curl -sf "${auth[@]}" \
  "${GH_PROXY}/repos/${REPO}/actions/runs/${run_id}/artifacts" \
  | python3 -c "
import sys, json
arts = json.load(sys.stdin)['artifacts']
match = [a for a in arts if a['name'] == '${ARTIFACT_NAME}']
if not match:
    print('ERROR: artifact not found', file=sys.stderr); sys.exit(1)
print(match[0]['id'])
")
echo "  Artifact: ${artifact_id}"

echo "→ Downloading..."
tmp_zip=$(mktemp /tmp/codex-XXXXXX.zip)
tmp_dir=$(mktemp -d /tmp/codex-install-XXXXXX)
curl -sfL "${auth[@]}" \
  "${GH_PROXY}/repos/${REPO}/actions/artifacts/${artifact_id}/zip" \
  -o "${tmp_zip}"

echo "→ Extracting..."
unzip -qo "${tmp_zip}" -d "${tmp_dir}"

echo "→ Installing to ${CODEX_DIR}/codex ..."
mkdir -p "${CODEX_DIR}"
# Can't overwrite a running binary; move the old one aside first.
[ -f "${CODEX_DIR}/codex" ] && mv "${CODEX_DIR}/codex" "${CODEX_DIR}/codex.prev"
cp "${tmp_dir}/codex" "${CODEX_DIR}/codex"
chmod +x "${CODEX_DIR}/codex"

echo "→ Cleaning up..."
rm -f "${tmp_zip}"
rm -rf "${tmp_dir}"

echo "✓ Installed: $("${CODEX_DIR}/codex" --version 2>&1)"
echo "  Restart the happy daemon to pick up the new binary."
