#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RUN_DIR="${HOST_RUN_DIR:-$ROOT_DIR/.host-run}"
LOG_DIR="${HOST_LOG_DIR:-$RUN_DIR/logs}"
PID_DIR="${HOST_PID_DIR:-$RUN_DIR/pids}"

MKCRI_BIN="${MKCRI_BIN:-$ROOT_DIR/mk-container/mkcri}"

MKCRI_CONTROL_DEVICE_PATH="${MKCRI_CONTROL_DEVICE_PATH:-/dev/mkring_container_bridge}"
MKCRI_STREAM_DEVICE_PATH="${MKCRI_STREAM_DEVICE_PATH:-/dev/mkring_stream_bridge}"
MKCRI_LISTEN_SOCKET="${MKCRI_LISTEN_SOCKET:-/tmp/mkcri.sock}"
MK_CONTROL_TRANSPORT="${MK_CONTROL_TRANSPORT:-mkring}"
MK_KERNEL_START_COMMAND="${MK_KERNEL_START_COMMAND:-/home/wzx/multikernel.sh}"
MK_KERNEL_STOP_COMMAND="${MK_KERNEL_STOP_COMMAND:-/home/wzx/stub_stop.sh}"

MKCRI_PID_FILE="$PID_DIR/mkcri.pid"
MKCRI_LOG_FILE="$LOG_DIR/mkcri.log"

die() {
	echo "start-host.sh: $*" >&2
	exit 1
}

validate_control_transport() {
	case "$MK_CONTROL_TRANSPORT" in
		mock|mkring) ;;
		*)
			die "unsupported MK_CONTROL_TRANSPORT: $MK_CONTROL_TRANSPORT"
			;;
	esac
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
		die "please run as root (needed for socket and kernel module setup)"
	fi
}

ensure_dirs() {
	mkdir -p "$RUN_DIR" "$LOG_DIR" "$PID_DIR"
	mkdir -p "$(dirname "$MKCRI_LISTEN_SOCKET")"
}

ensure_not_running() {
	check_pid_file "$MKCRI_PID_FILE" "mkcri"
}

ensure_kernel_commands() {
	[[ "$MK_CONTROL_TRANSPORT" == "mock" ]] && return 0
	require_file "$MK_KERNEL_START_COMMAND"
	require_file "$MK_KERNEL_STOP_COMMAND"
}

ensure_module_device() {
	local dev_name
	local stream_dev_name

	[[ "$MK_CONTROL_TRANSPORT" == "mock" ]] && return 0

	if [[ -e "$MKCRI_CONTROL_DEVICE_PATH" ]]; then
		:
	else
		dev_name="$(basename "$MKCRI_CONTROL_DEVICE_PATH")"
		modprobe mkring_container_bridge role=host "device_name=$dev_name"

		wait_for_path "$MKCRI_CONTROL_DEVICE_PATH" 5 || \
			die "device not ready after modprobe: $MKCRI_CONTROL_DEVICE_PATH"
	fi

	if [[ -e "$MKCRI_STREAM_DEVICE_PATH" ]]; then
		return 0
	fi

	stream_dev_name="$(basename "$MKCRI_STREAM_DEVICE_PATH")"
	modprobe mkring_stream_bridge "device_name=$stream_dev_name"

	wait_for_path "$MKCRI_STREAM_DEVICE_PATH" 5 || \
		die "device not ready after modprobe: $MKCRI_STREAM_DEVICE_PATH"
}

ensure_daxfs_started() {
	insmod "/home/wzx/daxfs/daxfs/daxfs/daxfs.ko" || true
	"/home/wzx/daxfs/daxfs/tools/mkdaxfs" -d "/home/wzx/daxfs/daxfs-share" -D /dev/mem -p 0x250000000 -s 256M
	mkdir -p /mnt/daxfs || true
	mount -t daxfs -o phys=0x250000000,size=268435456 none /mnt/daxfs
	echo "daxfs mount on /mnt/daxfs"
}

start_mkcri() {
	rm -f "$MKCRI_LISTEN_SOCKET"

	nohup env \
		MK_CONTROL_TRANSPORT="$MK_CONTROL_TRANSPORT" \
		MKCRI_CONTROL_DEVICE_PATH="$MKCRI_CONTROL_DEVICE_PATH" \
		MKCRI_LISTEN_SOCKET="$MKCRI_LISTEN_SOCKET" \
		MKCRI_STREAM_DEVICE_PATH="$MKCRI_STREAM_DEVICE_PATH" \
		MK_KERNEL_START_COMMAND="$MK_KERNEL_START_COMMAND" \
		MK_KERNEL_STOP_COMMAND="$MK_KERNEL_STOP_COMMAND" \
		"$MKCRI_BIN" \
		>>"$MKCRI_LOG_FILE" 2>&1 &

	echo $! >"$MKCRI_PID_FILE"

	wait_for_unix_socket "$MKCRI_LISTEN_SOCKET" 5 || {
		kill "$(cat "$MKCRI_PID_FILE")" 2>/dev/null || true
		rm -f "$MKCRI_PID_FILE"
		die "mkcri did not create socket: $MKCRI_LISTEN_SOCKET"
	}
}

print_summary() {
	cat <<EOF
host services started

mkcri:
  pid file: $MKCRI_PID_FILE
  log file: $MKCRI_LOG_FILE
  socket:   $MKCRI_LISTEN_SOCKET

effective config:
  MK_CONTROL_TRANSPORT=$MK_CONTROL_TRANSPORT
  MKCRI_CONTROL_DEVICE_PATH=$MKCRI_CONTROL_DEVICE_PATH
  MKCRI_STREAM_DEVICE_PATH=$MKCRI_STREAM_DEVICE_PATH
  MK_KERNEL_START_COMMAND=$MK_KERNEL_START_COMMAND
  MK_KERNEL_STOP_COMMAND=$MK_KERNEL_STOP_COMMAND
EOF
}

main() {
	ensure_root
	validate_control_transport
	require_file "$MKCRI_BIN"
	ensure_kernel_commands
	ensure_dirs
	ensure_not_running
	ensure_module_device
	ensure_daxfs_started
	start_mkcri
	print_summary
}

main "$@"
