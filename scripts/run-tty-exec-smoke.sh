#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

MKCRI_SOCKET="${MKCRI_SOCKET:-/tmp/mkcri.sock}"
MKEXECURL_BIN="${MKEXECURL_BIN:-$ROOT_DIR/mk-container/mkexecurl}"
CURL_BIN="${CURL_BIN:-curl}"
CONTAINER_ID="${1:-}"
EXEC_SCRIPT="${EXEC_SCRIPT:-echo TTY_EXEC_OK; uname -a; exit}"

usage() {
	cat <<EOF
usage: $(basename "$0") <container-id> [shell-command...]

This is a raw HTTP tty-exec smoke test for mkcri's current exec path.
It does not use crictl exec -it yet.

Environment:
  MKCRI_SOCKET   CRI runtime socket (default: /tmp/mkcri.sock)
  MKEXECURL_BIN  mkexecurl helper path (default: $ROOT_DIR/mk-container/mkexecurl)
  EXEC_SCRIPT    shell snippet written to tty stdin

Examples:
  $(basename "$0") mkc-123456
  EXEC_SCRIPT='echo hello; id; exit' $(basename "$0") mkc-123456
EOF
}

if [[ -z "$CONTAINER_ID" ]]; then
	usage >&2
	exit 1
fi

if [[ ! -x "$MKEXECURL_BIN" ]]; then
	cat >&2 <<EOF
run-tty-exec-smoke.sh: missing helper binary: $MKEXECURL_BIN

Build it with:
  cd "$ROOT_DIR/mk-container"
  go build -o mkexecurl ./cmd/mkexecurl
EOF
	exit 1
fi

if ! command -v "$CURL_BIN" >/dev/null 2>&1; then
	echo "run-tty-exec-smoke.sh: curl not found: $CURL_BIN" >&2
	exit 1
fi

URL="$("$MKEXECURL_BIN" -socket "$MKCRI_SOCKET" -container "$CONTAINER_ID" -- sh)"

echo "CONTAINER_ID=$CONTAINER_ID"
echo "EXEC_URL=$URL"
echo "================================================="
echo "raw tty exec response follows"
echo "================================================="

printf '%s\n' "$EXEC_SCRIPT" | "$CURL_BIN" -sS -N -X POST --data-binary @- "$URL"

echo
echo "================================================="
echo "exec smoke test completed"
echo "================================================="
