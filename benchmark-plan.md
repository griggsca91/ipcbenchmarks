# IPC Benchmark Plan: 5 KB Random Payload

> **Goal**: Measure round-trip latency and throughput for a random 5 KB payload sent from main container → sidecar → main container (ping-pong), for each IPC method.

---

## Common Test Harness

### Payload
- **5,120 bytes** of cryptographically random data, generated once at startup and reused for all iterations
- Sidecar echoes the exact payload back (validates integrity via checksum on first + last iteration)

### Metrics to Capture
| Metric | How |
|--------|-----|
| **Latency (round-trip)** | `clock_gettime(CLOCK_MONOTONIC)` around each send+receive pair |
| **P50 / P95 / P99 / max** | Collect all individual latencies in an array, sort, compute percentiles |
| **Throughput (msg/sec)** | Total messages ÷ wall-clock time |
| **CPU usage** | `getrusage(RUSAGE_SELF)` before/after — user + system time |
| **Syscall count** | `strace -c` on a short run (1,000 iterations) for syscall profile |

### Test Protocol
1. **Warmup**: 10,000 round-trips (discarded)
2. **Measurement**: 100,000 round-trips (recorded individually)
3. **Cooldown**: flush, close, report
4. **Concurrency sweep**: Run with 1, 4, 16 concurrent sender goroutines/threads (where the method supports it)
5. **Repeat**: 3 runs per configuration, report median of medians

### Container Images
- **Language**: Go (consistent across all methods, good syscall-level control, native support for all IPC types)
- **Base image**: `golang:1.22-bookworm` for build, `debian:bookworm-slim` for runtime
- Two binaries: `ipc-main` (initiator) and `ipc-sidecar` (responder)
- Shared library `ipc-bench` with common timing, stats, and payload logic

### Kubernetes Pod Template (base)
```yaml
apiVersion: v1
kind: Pod
metadata:
  name: ipc-bench-METHOD
spec:
  restartPolicy: Never
  # volumes injected per-method below
  containers:
  - name: main
    image: ipc-bench:latest
    command: ["./ipc-main", "--method=METHOD", "--size=5120", "--count=100000", "--warmup=10000"]
    resources:
      requests:
        cpu: "1"
        memory: "256Mi"
      limits:
        cpu: "1"
        memory: "256Mi"
  - name: sidecar
    image: ipc-bench:latest
    command: ["./ipc-sidecar", "--method=METHOD", "--size=5120"]
    resources:
      requests:
        cpu: "1"
        memory: "256Mi"
      limits:
        cpu: "1"
        memory: "256Mi"
```

---

## Method 1: TCP Loopback (127.0.0.1)

### Kubernetes Config
No additional volumes needed — containers share the network namespace by default.

### Implementation Plan

**Sidecar (server)**:
1. `net.Listen("tcp", "127.0.0.1:9000")`
2. Accept connection, set `TCP_NODELAY` via `SetNoDelay(true)`
3. Loop: read exactly 5,120 bytes → write back 5,120 bytes

**Main (client)**:
1. `net.Dial("tcp", "127.0.0.1:9000")`, set `TCP_NODELAY`
2. Loop: record start time → `Write(payload)` → `ReadFull(buf, 5120)` → record end time

### Validation Checklist
- [ ] Confirm `TCP_NODELAY` is set (critical — Nagle adds up to 40 ms)
- [ ] Verify full 5,120 bytes received each round-trip (use `io.ReadFull`)
- [ ] Checksum first and last payload to confirm integrity
- [ ] Compare with/without `TCP_NODELAY` to quantify Nagle impact

### Expected Baseline
~7–10 µs round-trip for 5 KB (slightly higher than 100-byte benchmark due to larger payload)

---

## Method 2: gRPC over UDS

### Kubernetes Config
```yaml
volumes:
- name: socket-dir
  emptyDir:
    medium: Memory
```
Both containers mount at `/run/ipc`.

### Implementation Plan

**Proto definition**:
```protobuf
syntax = "proto3";
service Bench {
  rpc Echo(Payload) returns (Payload);
  rpc EchoStream(stream Payload) returns (stream Payload);  // for streaming test
}
message Payload {
  bytes data = 1;
}
```

**Sidecar (server)**:
1. Create UDS listener at `unix:///run/ipc/grpc.sock`
2. Implement `Echo` — return received payload as-is
3. Implement `EchoStream` — for each received message, send it back

**Main (client)**:
1. `grpc.Dial("unix:///run/ipc/grpc.sock")`
2. **Unary test**: Loop: record start → `Echo(payload)` → record end
3. **Streaming test**: Open `EchoStream`, loop: record start → `Send(payload)` → `Recv()` → record end

### Validation Checklist
- [ ] Test both unary and bidirectional streaming RPCs
- [ ] Set `MaxConcurrentStreams` to 1000 (default 100 can bottleneck)
- [ ] Measure serialization overhead separately: time `proto.Marshal` / `proto.Unmarshal` in isolation
- [ ] Verify payload bytes match after deserialization

### Expected Baseline
- Unary: ~120–200 µs (protobuf ser/deser + HTTP/2 framing overhead)
- Streaming: significantly lower per-message after stream setup

---

## Method 3: Unix Domain Sockets (SOCK_SEQPACKET)

### Kubernetes Config
```yaml
volumes:
- name: socket-dir
  emptyDir:
    medium: Memory
```
Both containers mount at `/run/ipc`.

### Implementation Plan

**Sidecar (server)**:
1. Create socket: `syscall.Socket(AF_UNIX, SOCK_SEQPACKET, 0)`
2. Bind to `/run/ipc/bench.sock`, listen, accept
3. Loop: `Recvmsg` (read exactly one message) → `Sendmsg` (echo back)

**Main (client)**:
1. Connect to `/run/ipc/bench.sock` with `SOCK_SEQPACKET`
2. Loop: record start → `Sendmsg(payload)` → `Recvmsg(buf)` → record end

**Note**: Go's `net` package doesn't expose `SOCK_SEQPACKET` directly — use `golang.org/x/sys/unix` for raw syscalls, or use the `github.com/mdlayher/socket` package.

### Validation Checklist
- [ ] Confirm message boundaries preserved (receive exactly 5,120 bytes per recv, not partial)
- [ ] Test `SOCK_SEQPACKET` vs `SOCK_STREAM` (stream needs length-prefix framing)
- [ ] Verify socket buffer sizes: `SO_SNDBUF` / `SO_RCVBUF` (default ~208 KB on Linux)
- [ ] Increase socket buffer if needed: `setsockopt(SO_SNDBUF, 262144)`
- [ ] Checksum validation on first/last message

### Expected Baseline
~2–4 µs round-trip for 5 KB (5 KB is well within default buffer sizes)

---

## Method 4: Named Pipes (FIFOs)

### Kubernetes Config
```yaml
volumes:
- name: pipe-dir
  emptyDir: {}
initContainers:
- name: create-pipes
  image: busybox
  command: ["sh", "-c", "mkfifo /pipes/req && mkfifo /pipes/resp"]
  volumeMounts:
  - name: pipe-dir
    mountPath: /pipes
```
Both containers mount at `/pipes`.

### Implementation Plan

**Two FIFOs needed** (pipes are unidirectional):
- `/pipes/req` — main writes, sidecar reads
- `/pipes/resp` — sidecar writes, main reads

**Sidecar**:
1. Open `/pipes/req` for reading, `/pipes/resp` for writing
2. Loop: read length-prefixed message from `req` → write to `resp`

**Main**:
1. Open `/pipes/req` for writing, `/pipes/resp` for reading
2. Loop: record start → write length-prefixed payload to `req` → read response from `resp` → record end

**Critical: 5 KB > PIPE_BUF (4,096 bytes)**
- Writes of 5,120 bytes are **NOT atomic** on a named pipe (PIPE_BUF = 4,096)
- Must use a length-prefix protocol: write 4-byte length header + 5,120 bytes payload
- Single-writer only — concurrent writers will interleave (test only with concurrency=1)

### Validation Checklist
- [ ] Confirm pipe buffer is sufficient (default 64 KB >> 5 KB, so OK)
- [ ] Implement length-prefix framing (4-byte big-endian length + payload)
- [ ] Verify reads are complete (may need multiple `read()` calls since > PIPE_BUF)
- [ ] Only test concurrency=1 (FIFOs are not safe for concurrent writers)
- [ ] Open order matters: sidecar must open `req` for read before main opens for write (or both block). Use goroutines or nonblocking open.

### Expected Baseline
~5–8 µs round-trip for 5 KB (slightly higher than 100-byte due to non-atomic writes requiring framing)

---

## Method 5: POSIX Message Queues

### Kubernetes Config
No volumes needed — containers share IPC namespace by default.

**But**: default `msgsize_max` is 8,192 bytes. Our 5 KB payload (5,120 bytes) fits, but barely. Verify at runtime.

```yaml
# If msgsize_max needs increasing, use privileged initContainer:
initContainers:
- name: tune-mqueue
  image: busybox
  securityContext:
    privileged: true
  command: ["sh", "-c", "echo 16384 > /proc/sys/fs/mqueue/msgsize_max && echo 256 > /proc/sys/fs/mqueue/msg_max"]
```

### Implementation Plan

**Two queues needed** (or one per direction):
- `/bench-req` — main sends, sidecar receives
- `/bench-resp` — sidecar sends, main receives

**Sidecar**:
1. `mq_open("/bench-req", O_RDONLY)` and `mq_open("/bench-resp", O_WRONLY | O_CREAT)`
2. Loop: `mq_receive(req_q)` → `mq_send(resp_q, payload, 5120, 0)`

**Main**:
1. `mq_open("/bench-req", O_WRONLY | O_CREAT)` and `mq_open("/bench-resp", O_RDONLY)`
2. Loop: record start → `mq_send(req_q, payload, 5120, 0)` → `mq_receive(resp_q, buf)` → record end

**Go note**: No native Go package for POSIX mqueues. Options:
- Use CGo wrapper around `<mqueue.h>` (link with `-lrt`)
- Use `github.com/narslan/mqueue` or write thin CGo bindings
- Alternative: write this benchmark in C for accuracy

### Validation Checklist
- [ ] Check `msgsize_max >= 5120` at startup, fail fast if not
- [ ] Check `msg_max` (queue depth) — default 10 is very shallow, may cause blocking
- [ ] Verify queue cleanup: `mq_unlink` in defer/shutdown handler
- [ ] Test both blocking and non-blocking (`O_NONBLOCK`) modes
- [ ] Measure impact of priority parameter (set all to 0 for fair comparison)

### Expected Baseline
~4–6 µs round-trip for 5 KB (higher than 100-byte benchmarks due to larger kernel copy)

---

## Method 6: Shared Memory + Lock-Free Ring Buffer

### Kubernetes Config
```yaml
volumes:
- name: dshm
  emptyDir:
    medium: Memory
    sizeLimit: 64Mi
```
Both containers mount at `/dev/shm`.

### Implementation Plan

**Ring buffer design** (per direction — 2 ring buffers total):
```
┌────────────────────────────────────────────┐
│ Header (cache-line aligned, 64 bytes)      │
│   write_idx  uint64 (atomic)               │
│   read_idx   uint64 (atomic)               │
│   slot_count uint32                        │
│   slot_size  uint32                        │
├────────────────────────────────────────────┤
│ Slot 0:  [4-byte len][5120 bytes data]     │
│ Slot 1:  [4-byte len][5120 bytes data]     │
│ ...                                        │
│ Slot N-1                                   │
└────────────────────────────────────────────┘
```

- **Slot count**: 256 (power of 2 for fast modulo via bitmask)
- **Slot size**: 5,124 bytes (4-byte length + 5,120 data), padded to 8-byte boundary = 5,128
- **Total per ring**: 64 + (256 × 5,128) = ~1.28 MB
- **Two rings** (req + resp): ~2.56 MB total

**Shared memory setup** (either process creates, other attaches):
1. `shm_open("/bench-ring-req", O_CREAT | O_RDWR)` → `ftruncate` → `mmap`
2. `shm_open("/bench-ring-resp", O_CREAT | O_RDWR)` → `ftruncate` → `mmap`

**Write (producer)**:
```
1. Load write_idx (relaxed)
2. Check slot not full: write_idx - read_idx < slot_count
3. Copy payload into slot[write_idx % slot_count]
4. Store write_idx + 1 (release barrier)
```

**Read (consumer)**:
```
1. Spin-wait: load write_idx (acquire) > read_idx
2. Copy payload from slot[read_idx % slot_count]
3. Store read_idx + 1 (release barrier)
```

**Go note**: Use `unsafe.Pointer` + `sync/atomic` for atomic operations, or write the hot loop in C via CGo, or use `golang.org/x/sys/unix` for `mmap`/`shm_open`.

**Alternative**: Use a proven library instead of hand-rolling:
- Evaluate `github.com/cloudwego/shmipc-go` for the Go implementation
- Wrap iceoryx2 via CGo for C/Rust-quality ring buffer

### Validation Checklist
- [ ] Verify cache-line alignment (64 bytes) for `write_idx` and `read_idx` to prevent false sharing
- [ ] Confirm memory ordering: `StoreRelease` on write_idx, `LoadAcquire` on read_idx
- [ ] Test with `GODEBUG=msan=1` or run under ThreadSanitizer (via C build) to catch races
- [ ] Validate payload integrity with checksum on first/last iteration
- [ ] Measure spin-wait CPU cost: compare busy-poll vs `runtime.Gosched()` backoff vs futex-based wakeup
- [ ] Clean up shared memory segments on exit: `shm_unlink`

### Expected Baseline
~200–500 ns round-trip for 5 KB (memcpy of 5 KB ≈ 100–200 ns, plus atomic overhead)

---

## Execution Plan

### Phase 1: Scaffolding
1. Create Go module `ipc-bench` with shared timing/stats/payload package
2. Build Dockerfile (multi-stage: build all binaries, slim runtime image)
3. Create Kubernetes namespace `ipc-bench`

### Phase 2: Implement & Validate (one method at a time)
For each method:
1. Implement sidecar responder
2. Implement main initiator
3. Run locally first (two processes, same machine) to validate correctness
4. Deploy to Kubernetes, run benchmark
5. Collect results, verify against expected baseline

### Phase 3: Cross-Method Comparison
1. Run all 6 methods on the same node (pin pod to node with `nodeSelector`)
2. Collect into unified results table
3. Generate latency distribution charts (histogram of per-message latencies)

### Implementation Order
1. **TCP loopback** — simplest, validates harness
2. **UDS SOCK_SEQPACKET** — standard approach, validates socket harness
3. **Named Pipes** — validates filesystem-based approach
4. **POSIX Message Queues** — validates IPC namespace
5. **gRPC over UDS** — validates RPC layer
6. **Shared Memory** — most complex, do last

### Results Template
```
Method: ___
Payload: 5,120 bytes
Iterations: 100,000 (after 10,000 warmup)
Concurrency: 1 | 4 | 16

Latency (round-trip):
  P50:  ___ µs
  P95:  ___ µs
  P99:  ___ µs
  Max:  ___ µs

Throughput: ___ msg/sec
CPU time:   ___ user + ___ sys (per 100k msgs)
Syscalls:   ___ (per round-trip, from strace)
```
