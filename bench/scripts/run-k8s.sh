#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
BENCH_DIR="$(dirname "$SCRIPT_DIR")"
K8S_DIR="$BENCH_DIR/k8s"
NAMESPACE="ipc-bench"

METHODS=${@:-tcp uds fifo mqueue grpc shm}

echo "=== IPC Benchmark Suite (Kubernetes) ==="

# Build image
echo "Building Docker image..."
docker build -t ipc-bench:latest "$BENCH_DIR"

# Load into kind
echo "Loading image into kind..."
kind load docker-image ipc-bench:latest

# Create namespace
kubectl create ns "$NAMESPACE" --dry-run=client -o yaml | kubectl apply -f -

# Run each method
for method in $METHODS; do
    MANIFEST="$K8S_DIR/$method.yaml"
    POD_NAME="ipc-bench-$method"

    if [ ! -f "$MANIFEST" ]; then
        echo "Skipping $method: no manifest at $MANIFEST"
        continue
    fi

    echo ""
    echo "--- Running: $method ---"

    # Clean up previous run
    kubectl delete pod "$POD_NAME" -n "$NAMESPACE" --ignore-not-found --wait=false 2>/dev/null || true
    sleep 1

    # Deploy
    kubectl apply -f "$MANIFEST"

    # Wait for main container to complete (it will terminate the pod)
    echo "  Waiting for benchmark to complete..."
    if kubectl wait --for=jsonpath='{.status.containerStatuses[?(@.name=="main")].state.terminated}' \
        pod/"$POD_NAME" -n "$NAMESPACE" --timeout=300s 2>/dev/null; then
        echo "  Results:"
        kubectl logs "$POD_NAME" -c main -n "$NAMESPACE" 2>/dev/null | grep -v "^2"
    else
        echo "  TIMEOUT or ERROR"
        kubectl describe pod "$POD_NAME" -n "$NAMESPACE" | tail -20
    fi
done

echo ""
echo "=== Done ==="
echo "Clean up: kubectl delete ns $NAMESPACE"
