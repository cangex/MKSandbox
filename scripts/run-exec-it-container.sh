#!/usr/bin/env bash
set -euo pipefail

POD_NAME="${POD_NAME:-mkpod-execit}"
POD_NAMESPACE="${POD_NAMESPACE:-default}"
POD_UID="${POD_UID:-mkpod-execit-001}"
POD_ATTEMPT="${POD_ATTEMPT:-1}"
LOG_DIR="${LOG_DIR:-/tmp/mkpod-execit}"

CONTAINER_NAME="${CONTAINER_NAME:-shellbox}"
CONTAINER_ATTEMPT="${CONTAINER_ATTEMPT:-1}"
IMAGE="${IMAGE:-docker.1ms.run/busybox:latest}"
LOG_PATH="${LOG_PATH:-shellbox.log}"
COMMAND_0="${COMMAND_0:-sh}"
COMMAND_1="${COMMAND_1:--c}"
ARGS_0="${ARGS_0:-i=0; while true; do echo tick-\$i; i=\$((i+1)); sleep 1; done}"

POD_CONFIG="$(mktemp /tmp/pod-execit-config.XXXXXX.json)"
CONTAINER_CONFIG="$(mktemp /tmp/container-execit-config.XXXXXX.json)"

cleanup() {
	rm -f "$POD_CONFIG" "$CONTAINER_CONFIG"
}
trap cleanup EXIT

mkdir -p "$LOG_DIR"

cat >"$POD_CONFIG" <<EOF
{
  "metadata": {
    "name": "${POD_NAME}",
    "namespace": "${POD_NAMESPACE}",
    "uid": "${POD_UID}",
    "attempt": ${POD_ATTEMPT}
  },
  "log_directory": "${LOG_DIR}"
}
EOF

cat >"$CONTAINER_CONFIG" <<EOF
{
  "metadata": {
    "name": "${CONTAINER_NAME}",
    "attempt": ${CONTAINER_ATTEMPT}
  },
  "image": {
    "image": "${IMAGE}"
  },
  "command": [
    "${COMMAND_0}",
    "${COMMAND_1}"
  ],
  "args": [
    "${ARGS_0}"
  ],
  "log_path": "${LOG_PATH}"
}
EOF

echo "run-exec-it-container.sh: creating pod sandbox" >&2
POD_ID="$(crictl runp "$POD_CONFIG")"
echo "POD_ID=${POD_ID}"

echo "run-exec-it-container.sh: creating container ${CONTAINER_NAME}" >&2
CONTAINER_ID="$(crictl create "${POD_ID}" "$CONTAINER_CONFIG" "$POD_CONFIG")"
echo "CONTAINER_ID=${CONTAINER_ID}"

echo "run-exec-it-container.sh: starting container ${CONTAINER_ID}" >&2
crictl start "${CONTAINER_ID}" >/dev/null

if command -v jq >/dev/null 2>&1; then
	POD_IP="$(crictl inspectp "${POD_ID}" | jq -r '.status.network.ip')"
	echo "POD_IP=${POD_IP}"
fi

LOG_FILE="${LOG_DIR}/${LOG_PATH}"
echo "LOG_FILE=${LOG_FILE}"

echo "run-exec-it-container.sh: waiting for container to become RUNNING" >&2
for _ in $(seq 1 100); do
	STATE="$(crictl inspect "${CONTAINER_ID}" 2>/dev/null | jq -r '.status.state // empty' 2>/dev/null || true)"
	if [[ "$STATE" == "CONTAINER_RUNNING" ]]; then
		break
	fi
	sleep 0.1
done

echo "================================================="
echo "exec-it container is ready"
echo "================================================="
echo "CONTAINER_ID=${CONTAINER_ID}"
echo "POD_ID=${POD_ID}"
echo "LOG_FILE=${LOG_FILE}"
echo
echo "Suggested checks:"
echo "  crictl inspect ${CONTAINER_ID}"
echo "  tail -f ${LOG_FILE}"
echo
echo "Interactive validation:"
echo "  crictl exec -it ${CONTAINER_ID} sh"
echo
echo "Inside the shell, run:"
echo "  echo CRI_TTY_OK"
echo "  uname -a"
echo "  stty size"
echo "  exit"
echo
echo "Non-zero exit validation:"
echo "  crictl exec -it ${CONTAINER_ID} sh -lc 'exit 23'"
echo
echo "Cleanup:"
echo "  crictl stop ${CONTAINER_ID}"
echo "  crictl stopp ${POD_ID}   # if supported by your crictl"
echo "================================================="
