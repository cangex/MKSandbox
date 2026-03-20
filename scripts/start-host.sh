#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${HOST_RUN_DIR:-$ROOT_DIR/.host-run}"
LOG_DIR="${HOST_LOG_DIR:-$RUN_DIR/logs}"
PID_DIR="${HOST_PID_DIR:-$RUN_DIR/pids}"

MKRING_BRIDGE_BIN="${MKRING_BRIDGE_BIN:-$ROOT_DIR/mkring-bridge/mkring-bridge}"
MKCRI_BIN="${MKCRI_BIN:-$ROOT_DIR/mk-container/mkcri}"

MKRING_BRIDGE_TRANSPORT="${MKRING_BRIDGE_TRANSPORT:-device}"
MKRING_BRIDGE_DEVICE_PATH="${MKRING_BRIDGE_DEVICE_PATH:-/dev/mkring_container_bridge}"
MKRING_BRIDGE_LISTEN_SOCKET="${MKRING_BRIDGE_LISTEN_SOCKET:-/run/mk-container/mkring-bridge.sock}"

MKCRI_LISTEN_SOCKET="${MKCRI_LISTEN_SOCKET:-/tmp/mkcri.sock}"
MK_CONTROL_TRANSPORT="${MK_CONTROL_TRANSPORT:-mkring}"
MK_MKRING_BRIDGE_SOCKET="${MK_MKRING_BRIDGE_SOCKET:-$MKRING_BRIDGE_LISTEN_SOCKET}"
MK_KERNEL_START_COMMAND="${MK_KERNEL_START_COMMAND:-/usr/local/bin/start-subkernel}"
MK_KERNEL_STOP_COMMAND="${MK_KERNEL_STOP_COMMAND:-/usr/local/bin/stop-subkernel}"

BRIDGE_PID_FILE="$PID_DIR/mkring-bridge.pid"
MKCRI_PID_FILE="$PID_DIR/mkcri.pid"
BRIDGE_LOG_FILE="$LOG_DIR/mkring-bridge.log"
MKCRI_LOG_FILE="$LOG_DIR/mkcri.log"

die() {
	echo "start-host.sh: $*" >&2
	exit 1
}

pid_matches_process() {
	local pid="$1"
	local expected_name="$2"
	local exe_path=""
	local cmdline=""

	[[ "$pid" =~ ^[0-9]+$ ]] || return 1
	kill -0 "$pid" 2>/dev/null || return 1

	if [[ -L "/proc/$pid/exe" ]]; then
		exe_path="$(readlink -f "/proc/$pid/exe" 2>/dev/null || true)"
		[[ -n "$exe_path" ]] && [[ "$(basename "$exe_path")" == "$expected_name" ]] && return 0
	fi

	if [[ -r "/proc/$pid/cmdline" ]]; then
		cmdline="$(tr '\0' ' ' <"/proc/$pid/cmdline" 2>/dev/null || true)"
		[[ "$cmdline" == *"$expected_name"* ]] && return 0
	fi

	return 1
}

check_pid_file() {
	local pid_file="$1"
	local expected_name="$2"
	local pid=""

	[[ -f "$pid_file" ]] || return 0

	pid="$(<"$pid_file")"
	if pid_matches_process "$pid" "$expected_name"; then
		die "$expected_name is already running (pid $pid); stop it first"
	fi

	echo "start-host.sh: removing stale pid file $pid_file" >&2
	rm -f "$pid_file"
}

require_file() {
	local path="$1"
	[[ -f "$path" ]] || die "missing file: $path"
	[[ -x "$path" ]] || die "file is not executable: $path"
}

wait_for_path() {
	local path="$1"
	local timeout_sec="${2:-5}"
	local elapsed=0

	while (( elapsed < timeout_sec * 10 )); do
		[[ -e "$path" ]] && return 0
		sleep 0.1
		((elapsed += 1))
	done
	return 1
}

wait_for_unix_socket() {
	local path="$1"
	local timeout_sec="${2:-5}"
	local elapsed=0

	while (( elapsed < timeout_sec * 10 )); do
		[[ -S "$path" ]] && return 0
		sleep 0.1
		((elapsed += 1))
	done
	return 1
}

ensure_root() {
	if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
		die "please run as root (needed for /run and kernel module setup)"
	fi
}

ensure_dirs() {
	mkdir -p "$RUN_DIR" "$LOG_DIR" "$PID_DIR"
	mkdir -p "$(dirname "$MKRING_BRIDGE_LISTEN_SOCKET")"
	mkdir -p "$(dirname "$MKCRI_LISTEN_SOCKET")"
}

ensure_not_running() {
	check_pid_file "$BRIDGE_PID_FILE" "mkring-bridge"
	check_pid_file "$MKCRI_PID_FILE" "mkcri"
}

ensure_kernel_commands() {
	[[ "$MK_CONTROL_TRANSPORT" == "mkring" ]] || return 0
	require_file "$MK_KERNEL_START_COMMAND"
	require_file "$MK_KERNEL_STOP_COMMAND"
}

ensure_module_device() {
	local dev_name

	[[ "$MKRING_BRIDGE_TRANSPORT" == "device" ]] || return 0
	if [[ -e "$MKRING_BRIDGE_DEVICE_PATH" ]]; then
		return 0
	fi

	dev_name="$(basename "$MKRING_BRIDGE_DEVICE_PATH")"
	modprobe mkring_container_bridge role=host "device_name=$dev_name"

	wait_for_path "$MKRING_BRIDGE_DEVICE_PATH" 5 || \
		die "device not ready after modprobe: $MKRING_BRIDGE_DEVICE_PATH"
}

start_bridge() {
	rm -f "$MKRING_BRIDGE_LISTEN_SOCKET"

	nohup env \
		MKRING_BRIDGE_TRANSPORT="$MKRING_BRIDGE_TRANSPORT" \
		MKRING_BRIDGE_DEVICE_PATH="$MKRING_BRIDGE_DEVICE_PATH" \
		MKRING_BRIDGE_LISTEN_SOCKET="$MKRING_BRIDGE_LISTEN_SOCKET" \
		"$MKRING_BRIDGE_BIN" \
		>>"$BRIDGE_LOG_FILE" 2>&1 &

	echo $! >"$BRIDGE_PID_FILE"

	wait_for_unix_socket "$MKRING_BRIDGE_LISTEN_SOCKET" 5 || {
		kill "$(cat "$BRIDGE_PID_FILE")" 2>/dev/null || true
		rm -f "$BRIDGE_PID_FILE"
		die "mkring-bridge did not create socket: $MKRING_BRIDGE_LISTEN_SOCKET"
	}
}

start_mkcri() {
	rm -f "$MKCRI_LISTEN_SOCKET"

	nohup env \
		MK_CONTROL_TRANSPORT="$MK_CONTROL_TRANSPORT" \
		MK_MKRING_BRIDGE_SOCKET="$MK_MKRING_BRIDGE_SOCKET" \
		MKCRI_LISTEN_SOCKET="$MKCRI_LISTEN_SOCKET" \
		MK_KERNEL_START_COMMAND="$MK_KERNEL_START_COMMAND" \
		MK_KERNEL_STOP_COMMAND="$MK_KERNEL_STOP_COMMAND" \
		"$MKCRI_BIN" \
		>>"$MKCRI_LOG_FILE" 2>&1 &

	echo $! >"$MKCRI_PID_FILE"

	wait_for_unix_socket "$MKCRI_LISTEN_SOCKET" 5 || {
		kill "$(cat "$MKCRI_PID_FILE")" 2>/dev/null || true
		rm -f "$MKCRI_PID_FILE"
		kill "$(cat "$BRIDGE_PID_FILE")" 2>/dev/null || true
		rm -f "$BRIDGE_PID_FILE"
		die "mkcri did not create socket: $MKCRI_LISTEN_SOCKET"
	}
}

print_summary() {
	cat <<EOF
host services started

mkring-bridge:
  pid file: $BRIDGE_PID_FILE
  log file: $BRIDGE_LOG_FILE
  socket:   $MKRING_BRIDGE_LISTEN_SOCKET

mkcri:
  pid file: $MKCRI_PID_FILE
  log file: $MKCRI_LOG_FILE
  socket:   $MKCRI_LISTEN_SOCKET

effective config:
  MKRING_BRIDGE_TRANSPORT=$MKRING_BRIDGE_TRANSPORT
  MKRING_BRIDGE_DEVICE_PATH=$MKRING_BRIDGE_DEVICE_PATH
  MK_CONTROL_TRANSPORT=$MK_CONTROL_TRANSPORT
  MK_MKRING_BRIDGE_SOCKET=$MK_MKRING_BRIDGE_SOCKET
  MK_KERNEL_START_COMMAND=$MK_KERNEL_START_COMMAND
  MK_KERNEL_STOP_COMMAND=$MK_KERNEL_STOP_COMMAND
EOF
}

main() {
	ensure_root
	require_file "$MKRING_BRIDGE_BIN"
	require_file "$MKCRI_BIN"
	ensure_kernel_commands
	ensure_dirs
	ensure_not_running
	ensure_module_device
	start_bridge
	start_mkcri
	print_summary
}

main "$@"
