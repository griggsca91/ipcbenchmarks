#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Methods available on this platform
METHODS="tcp uds fifo grpc"
if [ "$(uname)" = "Linux" ]; then
    METHODS="tcp uds fifo mqueue grpc shm"
fi

echo "=== IPC Benchmark Suite ==="
echo "Platform: $(uname -s) $(uname -m)"
echo "Payload:  ${SIZE:-5120} bytes"
echo "Count:    ${COUNT:-100000}"
echo "Warmup:   ${WARMUP:-10000}"
echo ""

RESULTS_DIR=$(mktemp -d)
trap "rm -rf $RESULTS_DIR" EXIT

for method in $METHODS; do
    echo "--- Running: $method ---"
    if "$SCRIPT_DIR/run-local.sh" "$method" --json > "$RESULTS_DIR/$method.json" 2>/dev/null; then
        # Print a summary line from JSON
        python3 -c "
import json, sys
with open('$RESULTS_DIR/$method.json') as f:
    r = json.load(f)
print(f\"  {r['method']:8s}  P50={r['p50_ns']/1000:.1f}µs  P99={r['p99_ns']/1000:.1f}µs  throughput={r['throughput_rps']:.0f} rps\")
" 2>/dev/null || cat "$RESULTS_DIR/$method.json"
    else
        echo "  FAILED (method may not be supported on this platform)"
    fi
    echo ""
done

echo "=== Done ==="
echo "Raw JSON results in: $RESULTS_DIR/"
# Print all results
for f in "$RESULTS_DIR"/*.json; do
    [ -f "$f" ] && cat "$f"
done
