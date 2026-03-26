#!/usr/bin/env bash
set -euo pipefail

usage() {
	cat <<'EOF'
Usage: run-exec-it-container.sh [--kernel-boot-mode MODE]

Options:
  --kernel-boot-mode MODE   Set pod annotation mksandbox.io/kernel-boot-mode.
                            Supported values: cold_boot, snapshot
  -h, --help                Show this help message
EOF
}

KERNEL_BOOT_MODE="${KERNEL_BOOT_MODE:-cold_boot}"

while [[ $# -gt 0 ]]; do
	case "$1" in
	--kernel-boot-mode)
		if [[ $# -lt 2 ]]; then
			echo "[MKSandbox] Missing value for --kernel-boot-mode" >&2
			usage >&2
			exit 1
		fi
		KERNEL_BOOT_MODE="$2"
		shift 2
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		echo "[MKSandbox] Unknown argument: $1" >&2
		usage >&2
		exit 1
		;;
	esac
done

case "$KERNEL_BOOT_MODE" in
cold_boot|snapshot)
	;;
*)
	echo "[MKSandbox] Unsupported kernel boot mode: ${KERNEL_BOOT_MODE}" >&2
	echo "[MKSandbox] Supported values: cold_boot, snapshot" >&2
	exit 1
	;;
esac

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
EXEC_COMMAND="${EXEC_COMMAND:-sh}"

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
  "annotations": {
    "mksandbox.io/kernel-boot-mode": "${KERNEL_BOOT_MODE}"
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

echo "[MKSandbox] Creating pod sandbox..." >&2
POD_ID="$(crictl runp "$POD_CONFIG")"
echo "POD_ID=${POD_ID}"
echo "KERNEL_BOOT_MODE=${KERNEL_BOOT_MODE}"

echo "[MKSandbox] Creating container ${CONTAINER_NAME}..." >&2
CONTAINER_ID="$(crictl create "${POD_ID}" "$CONTAINER_CONFIG" "$POD_CONFIG")"
echo "CONTAINER_ID=${CONTAINER_ID}"

echo "[MKSandbox] Starting container ${CONTAINER_ID}..." >&2
crictl start "${CONTAINER_ID}" >/dev/null

if command -v jq >/dev/null 2>&1; then
	POD_IP="$(crictl inspectp "${POD_ID}" | jq -r '.status.network.ip')"
	echo "POD_IP=${POD_IP}"
fi

LOG_FILE="${LOG_DIR}/${LOG_PATH}"
echo "LOG_FILE=${LOG_FILE}"

echo "[MKSandbox] Waiting for container to become RUNNING..." >&2
for _ in $(seq 1 100); do
	STATE="$(crictl inspect "${CONTAINER_ID}" 2>/dev/null | jq -r '.status.state // empty' 2>/dev/null || true)"
	if [[ "$STATE" == "CONTAINER_RUNNING" ]]; then
		break
	fi
	sleep 0.1
done

echo "================================================="
echo "Interactive exec target is ready"
echo "================================================="
echo "CONTAINER_ID=${CONTAINER_ID}"
echo "POD_ID=${POD_ID}"
echo "KERNEL_BOOT_MODE=${KERNEL_BOOT_MODE}"
echo "LOG_FILE=${LOG_FILE}"
echo
echo "The script will now open:"
echo "  crictl exec -it ${CONTAINER_ID} ${EXEC_COMMAND}"
echo
echo "Suggested checks inside the shell:"
echo "  echo CRI_TTY_OK"
echo "  uname -a"
echo "  stty size"
echo "  exit"
echo "================================================="
echo

exec crictl exec -it "${CONTAINER_ID}" "${EXEC_COMMAND}"
