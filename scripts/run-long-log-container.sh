#!/usr/bin/env bash
set -euo pipefail

POD_NAME="${POD_NAME:-mkpod-longlog}"
POD_NAMESPACE="${POD_NAMESPACE:-default}"
POD_UID="${POD_UID:-mkpod-longlog-001}"
POD_ATTEMPT="${POD_ATTEMPT:-1}"
LOG_DIR="${LOG_DIR:-/tmp/mkpod-longlog}"

CONTAINER_NAME="${CONTAINER_NAME:-logger}"
CONTAINER_ATTEMPT="${CONTAINER_ATTEMPT:-1}"
IMAGE="${IMAGE:-docker.1ms.run/busybox:latest}"
LOG_PATH="${LOG_PATH:-logger.log}"
COMMAND_0="${COMMAND_0:-sh}"
COMMAND_1="${COMMAND_1:--c}"
ARGS_0="${ARGS_0:-i=0; while true; do echo tick-\$i; i=\$((i+1)); sleep 1; done}"

POD_CONFIG="$(mktemp /tmp/pod-longlog-config.XXXXXX.json)"
CONTAINER_CONFIG="$(mktemp /tmp/container-longlog-config.XXXXXX.json)"

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

echo "run-long-log-container.sh: creating pod sandbox" >&2
POD_ID="$(crictl runp "$POD_CONFIG")"
echo "POD_ID=${POD_ID}"

echo "run-long-log-container.sh: creating container ${CONTAINER_NAME}" >&2
CONTAINER_ID="$(crictl create "${POD_ID}" "$CONTAINER_CONFIG" "$POD_CONFIG")"
echo "CONTAINER_ID=${CONTAINER_ID}"

echo "run-long-log-container.sh: starting container ${CONTAINER_ID}" >&2
crictl start "${CONTAINER_ID}" >/dev/null

if command -v jq >/dev/null 2>&1; then
	POD_IP="$(crictl inspectp "${POD_ID}" | jq -r '.status.network.ip')"
	echo "POD_IP=${POD_IP}"
fi

LOG_FILE="${LOG_DIR}/${LOG_PATH}"
echo "LOG_FILE=${LOG_FILE}"
echo "NEXT_STEPS:"
echo "  crictl inspect ${CONTAINER_ID}"
echo "  crictl stop ${CONTAINER_ID}"
echo "  crictl stop ${CONTAINER_ID} && crictl stopp ${POD_ID}    # if your crictl supports stopp"
echo "  tail -f ${LOG_FILE}"

echo "run-long-log-container.sh: waiting for host log file" >&2
for _ in $(seq 1 300); do
	if [[ -f "$LOG_FILE" ]]; then
		break
	fi
	sleep 0.01
done

echo "================================================="
if [[ -f "$LOG_FILE" ]]; then
	echo "tail -f ${LOG_FILE}"
	tail -f "$LOG_FILE"
else
	echo "log file not found yet: $LOG_FILE" >&2
	echo "container is running; you can tail it manually later." >&2
fi
