#!/usr/bin/env bash
set -euo pipefail

METHOD=${1:?Usage: run-local.sh <method> [flags...]}
shift

SIZE=${SIZE:-5120}
COUNT=${COUNT:-100000}
WARMUP=${WARMUP:-10000}
SOCKET_PATH=$(mktemp -d)
ADDRESS="127.0.0.1:9000"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BENCH_DIR="$(dirname "$SCRIPT_DIR")"
BIN="$BENCH_DIR/ipc-bench"

# Build if binary doesn't exist or source is newer
if [ ! -f "$BIN" ] || [ "$BENCH_DIR/main.go" -nt "$BIN" ]; then
    echo "Building ipc-bench..." >&2
    (cd "$BENCH_DIR" && go build -o "$BIN" ./)
fi

cleanup() {
    if [ -n "${SIDECAR_PID:-}" ]; then
        kill "$SIDECAR_PID" 2>/dev/null || true
        wait "$SIDECAR_PID" 2>/dev/null || true
    fi
    rm -rf "$SOCKET_PATH"
}
trap cleanup EXIT

# Extract --parallel from extra args to pass to sidecar
PARALLEL=1
for arg in "$@"; do
    case "$arg" in
        --parallel=*) PARALLEL="${arg#*=}" ;;
    esac
done

# Start sidecar in background (stderr only, no pipe to avoid PID issues)
"$BIN" --role=sidecar --method="$METHOD" \
    --socket-path="$SOCKET_PATH" --address="$ADDRESS" \
    --size="$SIZE" --parallel="$PARALLEL" >/dev/null 2>&1 &
SIDECAR_PID=$!

# Give sidecar time to bind
sleep 0.5

# Run benchmark
"$BIN" --role=main --method="$METHOD" \
    --socket-path="$SOCKET_PATH" --address="$ADDRESS" \
    --size="$SIZE" --count="$COUNT" --warmup="$WARMUP" \
    "$@"
