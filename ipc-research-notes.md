# IPC Research: Kubernetes Sidecar Communication

> **Use case**: Main container ↔ long-running sidecar container (same pod). Sidecar guaranteed present at main container launch. Goal: minimize latency under many concurrent messages.

---

## Kubernetes Pod Fundamentals

### What's shared by default (no special config needed):
- **Network namespace** — shared loopback `127.0.0.1`
- **IPC namespace** — containers in the same pod already share System V IPC and POSIX shared memory (`shm_open`, `shmget`, semaphores, message queues)

### What requires explicit config:
| Config | When to use |
|--------|-------------|
| `shareProcessNamespace: true` | Only if a container needs to see the other's PIDs (signaling, ptrace, `/proc/PID/fd/`). NOT needed for data-transfer IPC. |
| `emptyDir medium: Memory` | Share socket files, FIFOs, or mmap files between containers via filesystem; expand `/dev/shm` beyond 64 MB default |
| `hostIPC: true` | **NEVER use** for same-pod IPC — exposes node-level IPC, serious security risk |

---

## Master Benchmark Table

### Latency (Round-Trip Ping-Pong)

| Method | Median Latency | P99 | Syscalls per 1M msgs |
|--------|---------------|-----|---------------------|
| Shared Memory (lock-free ring buffer) | **127–173 ns** | 850 ns | ~4 (setup/teardown only) |
| mmap file (emptyDir:Memory) | ~150–200 ns | ~1 µs | ~4 |
| Unix Domain Socket (UDS) | **1,439 ns (1.44 µs)** | 1,898 ns | ~2M |
| POSIX Message Queue | 2,741 ns | 12,000 ns | ~1.85M |
| Named Pipe (FIFO) | ~4,000 ns | ~5,000 ns | ~2M |
| Anonymous Pipe | 4,255 ns | 5,352 ns | ~2M |
| eventfd | 4,353 ns | 5,053 ns | ~2M |
| TCP loopback (TCP_NODELAY) | **7,287 ns (7.3 µs)** | 8,573 ns | ~2M |
| gRPC over UDS (unary call) | **116,000–167,000 ns** | 200,000 ns | high |
| HTTP/1.1 over TCP | ~100,000–1,000,000 ns | varies | very high |

### Throughput (Messages Per Second, ~100-byte messages)

| Method | Throughput | Notes |
|--------|-----------|-------|
| Shared Memory ring buffer | **4.7–8M msg/sec** | Zero-copy, zero syscalls |
| mmap file (emptyDir:Memory) | **5.3M msg/sec** | Near identical to shm |
| FIFO (Named Pipe) | 265,823 msg/sec | Sequential only |
| POSIX Message Queue | 232,253–364,823 msg/sec | Kernel-managed queue |
| Anonymous Pipe | 162,441 msg/sec | Not applicable across containers |
| Unix Domain Socket | 130,372 msg/sec | Standard production choice |
| TCP loopback | 70,221 msg/sec | Slowest socket option |
| gRPC over UDS | ~400k msg/sec | Amortized via streaming |

---

## Method Details

---

### 1. TCP / HTTP over localhost (127.0.0.1)

**Kubernetes config**: None — shared network namespace by default.

**How it works**: Full IP stack traversal. Headers, checksums, routing — even though both ends are on the same machine.

**Critical**: Without `TCP_NODELAY`, Nagle's algorithm can add **up to 40 ms** latency on small messages. Always set `SO_TCP_NODELAY` (or `SO_NODELAY`).

| Metric | Value |
|--------|-------|
| Latency | 7.3 µs median |
| Throughput | 70k msg/sec (single conn), 168k req/sec (nginx benchmark) |
| Concurrency | Good — multiple connections, HTTP keep-alive |
| Complexity | Low — universal library support |
| Zero-copy | No |
| Backpressure | Yes (TCP flow control) |

**Pros**:
- Zero config, every language/framework supports it
- Battle-tested, HTTP middlewares (retries, auth, logging)
- Works with Kubernetes health probes
- Compatible with service mesh (Envoy intercepts HTTP)

**Cons**:
- Slowest socket option (~5× slower than UDS)
- Full IP stack overhead (headers, checksums, routing)
- Nagle's algorithm pitfall on small messages
- Connection establishment cost

**Best for**: HTTP semantics, service-mesh compatibility, REST/JSON APIs.

---

### 2. gRPC over localhost

**Kubernetes config**: None for TCP transport. For UDS transport, add `emptyDir` volume.

**How it works**: Protobuf serialization + HTTP/2 framing + optional TLS. Can run over TCP or UDS.

| Metric | Value |
|--------|-------|
| Latency | 116–167 µs (unary RPC) — ~100× slower than raw UDS |
| Throughput | ~400k msg/sec across ~10 concurrent clients |
| Concurrency | Excellent — bidirectional streaming, multiplexing |
| Complexity | Medium — Protobuf schema required |
| Zero-copy | No |
| Backpressure | Yes |

**Gotcha**: Default 100 concurrent stream limit per HTTP/2 connection — can cause queuing under high concurrency. Increase with `MaxConcurrentStreams`.

**Pros**:
- Strong typing, schema evolution, language-agnostic codegen
- Bidirectional streaming, built-in deadlines/cancellation
- Excellent observability, service mesh integration

**Cons**:
- 100+ µs per unary call (Protobuf ser/deser + HTTP/2 framing)
- Complex setup vs raw sockets
- Overkill for simple high-frequency messaging

**Best for**: Service-style RPC with schema contracts, multi-language teams, service mesh integration.

---

### 3. Unix Domain Sockets (UDS) ⭐ Production Standard

**Kubernetes config**:
```yaml
volumes:
- name: socket-dir
  emptyDir:
    medium: Memory   # tmpfs — faster than default emptyDir
containers:
- name: main
  volumeMounts:
  - name: socket-dir
    mountPath: /run/ipc
- name: sidecar
  volumeMounts:
  - name: socket-dir
    mountPath: /run/ipc
```

**How it works**: Filesystem-addressed sockets that bypass the IP stack entirely. Data goes kernel→kernel without IP headers, checksums, or routing.

**Socket types**:
| Type | Description | Best for |
|------|-------------|----------|
| `SOCK_STREAM` | Connection-oriented, no message boundaries, backpressure | Streaming data |
| `SOCK_DGRAM` | Connectionless, message boundaries preserved, NO backpressure | Independent small msgs |
| `SOCK_SEQPACKET` | Connection-oriented + message boundaries + ordered | **Concurrent RPC-style — recommended** |

| Metric | Value |
|--------|-------|
| Latency | 1.44 µs median, 1.9 µs P99 |
| Throughput | 130k msg/sec (ping-pong), 192k req/sec (server benchmark) |
| Concurrency | Good — multiple connections or non-blocking I/O (epoll/io_uring) |
| Complexity | Low — standard socket API |
| Zero-copy | No (data copied kernel↔user space) |
| Backpressure | Yes (SOCK_STREAM, SOCK_SEQPACKET) |

**vs TCP**: 2× higher throughput, 5× lower latency, 40% better avg latency (PostgreSQL workload), 60% faster for connection-heavy workloads.

**Pros**:
- ~5× faster than TCP
- Universal socket API, every language supports it
- Supports concurrent connections naturally
- Natural backpressure, no message loss
- Industry standard: Envoy, Linkerd, CSI drivers, PostgreSQL, Redis all use UDS

**Cons**:
- Still requires 2 syscalls per round-trip
- Data copied kernel↔user (not zero-copy)
- 10× slower than shared memory

**Best for**: **THE standard production choice for sidecar communication.** Best balance of performance, simplicity, and ecosystem support.

---

### 4. Named Pipes (FIFOs)

**Kubernetes config**:
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
containers:
- name: main
  volumeMounts:
  - name: pipe-dir
    mountPath: /pipes
- name: sidecar
  volumeMounts:
  - name: pipe-dir
    mountPath: /pipes
```

**Key constraints**:
- **Unidirectional** — need 2 FIFOs for request/response
- **64 KB kernel buffer** (16 × 4 KB pages, Linux default)
- **Atomic writes ≤ 4096 bytes only** (PIPE_BUF). Larger writes may be interleaved.
- **NOT safe for concurrent writers** — messages interleave without application-level locking

| Metric | Value |
|--------|-------|
| Latency | ~4 µs |
| Throughput | 265k msg/sec (sequential) — drops with concurrent writers |
| Concurrency | Poor — single writer only for atomic delivery |
| Complexity | Low for simple use; hard for concurrent patterns |
| Zero-copy | No |
| Backpressure | Yes (writer blocks when 64 KB buffer full) |

**Pros**: Simple read/write API, natural backpressure, decent throughput for sequential streaming.
**Cons**: Unidirectional, 4 KB atomic write limit, broken for concurrent producers, poor fit for request/response.

**Best for**: One-directional log/event streaming main → sidecar. Not for concurrent bidirectional messaging.

---

### 5. POSIX Message Queues (/dev/mqueue)

**Kubernetes config**: Works by default (shared IPC namespace). Default limits are small — tune:
```bash
# Check/set limits (requires privileged initContainer or node-level config)
cat /proc/sys/fs/mqueue/msg_max      # default: 10 → increase to 256+
cat /proc/sys/fs/mqueue/msgsize_max  # default: 8192 bytes
```

**How it works**: Kernel-managed priority queue. Each `mq_send`/`mq_receive` is a syscall that copies data kernel↔user.

| Metric | Value |
|--------|-------|
| Latency | 2.7 µs avg, 12 µs P99 |
| Throughput | 232–365k msg/sec |
| Syscalls | 1.85M per 1M messages |
| Concurrency | Good — kernel handles queuing |
| Zero-copy | No |
| Backpressure | Yes (blocks when queue full) |

**Pros**: Kernel-managed queue (no ring buffer to implement), priority delivery, epoll/select compatible, async notification via signals, atomic message delivery.
**Cons**: Every send/receive is a syscall, 20× slower than shared memory, default limits need tuning, high syscall count, P99 latency spikes to 12 µs.

**Best for**: Simple async event delivery with priorities; when implementation simplicity trumps performance.

---

### 6. POSIX Shared Memory + mmap ⭐ Maximum Performance

**Kubernetes config** (recommended — overrides 64 MB default limit):
```yaml
volumes:
- name: dshm
  emptyDir:
    medium: Memory
    sizeLimit: 512Mi
containers:
- name: main
  volumeMounts:
  - name: dshm
    mountPath: /dev/shm
- name: sidecar
  volumeMounts:
  - name: dshm
    mountPath: /dev/shm
```

**Alternative** (uses default shared IPC namespace, limited to 64 MB):
```c
int fd = shm_open("/my-channel", O_CREAT | O_RDWR, 0600);
ftruncate(fd, sizeof(RingBuffer));
void *mem = mmap(NULL, sizeof(RingBuffer), PROT_READ|PROT_WRITE, MAP_SHARED, fd, 0);
```

**How it works**: After `mmap()` setup, writing to shared memory is literally a CPU `mov` instruction. No syscall, no data copy, no context switch. CPU cache coherency handles cross-core visibility.

| Metric | Value |
|--------|-------|
| Latency | **127–173 ns** avg (50× faster than UDS) |
| Throughput | **5–8M msg/sec** |
| Syscalls in hot path | **Zero** (4 total for entire session — setup/teardown only) |
| Context switches | ~0 |
| Concurrency | Excellent with lock-free MPMC ring buffer |
| Complexity | High — must implement synchronization |
| Zero-copy | Yes — true zero-copy |
| Backpressure | Manual — ring buffer overflow must be handled |

**Synchronization options** (from simplest to highest performance):
| Option | Latency | Syscalls | Notes |
|--------|---------|----------|-------|
| POSIX semaphores (`sem_init pshared=1`) | ~µs | 1 per op | Simple but kernel-crossing |
| Futex-based mutex (`PTHREAD_PROCESS_SHARED`) | ~µs | 1 per op | Same cost as semaphore |
| Lock-free atomic ring buffer | **~100 ns** | **Zero** | Best for high-frequency |
| Busy-polling with backoff | **~50 ns** | **Zero** | Lowest latency, burns CPU |

**Production libraries** (strongly prefer over rolling your own):
- [eclipse-iceoryx2](https://github.com/eclipse-iceoryx/iceoryx2) — C++/Rust, 50–300 ns, SPSC/MPMC, true zero-copy
- [shmipc-go](https://github.com/cloudwego/shmipc-spec) — Go, CloudWeGo (used in production at ByteDance)
- [lmax-disruptor](https://github.com/LMAX-Exchange/disruptor) — Java, ring buffer, ~20M msg/sec

**Pros**:
- Fastest IPC available (127–173 ns avg, 50× faster than UDS)
- True zero-copy
- Zero syscalls in data path
- Zero context switches
- Scales to any message size

**Cons**:
- Most complex to implement correctly (memory ordering is subtle)
- No built-in backpressure — must handle ring buffer overflow
- No built-in serialization
- Debugging is hard (no kernel-side visibility)
- Requires careful barrier/fence placement

**Best for**: Extreme low-latency requirements, >1M msg/sec, when willing to invest in implementation complexity.

---

### 7. io_uring (async I/O acceleration)

**Kubernetes config**: No config required. Check if available: `cat /proc/sys/kernel/io_uring_disabled` (0 = enabled).
> **Warning**: Many hardened clusters (GKE Autopilot, EKS with restricted seccomp) disable io_uring. Verify before relying on it.

**How it works**: Shared ring buffers between user space and kernel for submitting and completing I/O without per-operation syscalls.

**Key modes**:
- `IORING_SETUP_SQPOLL` — kernel polling thread; zero syscalls for I/O submission in hot path
- Registered buffers — avoids per-I/O buffer mapping overhead
- Fixed files — avoids per-I/O file descriptor lookup
- Linked operations — chain send→recv without user-space round-trip

| Metric | Value |
|--------|-------|
| Latency improvement | ~30% reduction vs epoll on same UDS |
| Throughput | 5M+ IOPS for file I/O, 30% less CPU for same socket throughput |
| Concurrency | Excellent |
| Complexity | High — use liburing |
| Availability | Kernel 5.1+ (full features 5.6+), often blocked by seccomp |

**Pros**: Reduces syscall overhead for high-concurrency I/O, batch submission, SQPOLL eliminates syscalls entirely, significant CPU efficiency gains.
**Cons**: Kernel 5.1+ required, disabled on many clusters via seccomp, complex API, best ecosystem support in C/C++/Rust.

**Best for**: Wrapping UDS communication in C/Rust when cluster io_uring is confirmed available.

---

### 8. eBPF SockMap Acceleration

**How it works**: Cilium's SockMap inserts socket objects into an eBPF map and uses `sk_msg` hooks to redirect `sendmsg()` calls directly between socket objects, bypassing the TCP/IP stack for localhost TCP.

| Metric | Value |
|--------|-------|
| Latency improvement | 18.28 µs → 12.99 µs TCP (~29% reduction) |
| Config | Cluster-level — requires Cilium CNI |
| Code changes | Zero — transparent to application |
| Benefit for UDS | None — UDS already bypasses IP stack |

**Pros**: Transparent (no application changes), brings TCP loopback near UDS performance.
**Cons**: Requires Cilium at cluster level, no benefit if already using UDS, not universally available.

---

### 9. System V IPC (shmget, msgget, semget)

Older kernel API for shared memory, message queues, and semaphores. Functionally equivalent to POSIX variants with clunkier API and harder cleanup semantics (segments persist until explicitly deleted or system restart).

**Verdict**: Use POSIX equivalents (`shm_open`, `mq_open`, `sem_open`) for all new code. System V IPC available in pod by default (shared IPC namespace).

---

### 10. RDMA / vsock

**RDMA**: Not applicable for same-pod IPC. Designed for cross-node transfers over specialized NICs. Same-pod containers share physical RAM — shared memory already IS direct memory access.

**vsock**: Not applicable for standard Kubernetes. Designed for VM↔hypervisor communication (Kata Containers, KubeVirt). Blocked by default in containerd 1.7+ seccomp profiles.

---

## Decision Framework

```
Need HTTP semantics or service mesh intercept?
  └── Yes → HTTP/1.1 or gRPC over TCP localhost

Need schema contracts, multi-language team?
  └── Yes → gRPC over UDS (add emptyDir volume)

Latency target < 10 µs, want simplicity?
  └── Yes → Unix Domain Sockets (SOCK_SEQPACKET)
            THE standard production sidecar approach

Latency target < 500 ns, >1M msg/sec?
  └── Yes → Shared Memory + lock-free ring buffer
            Use iceoryx2 (C++/Rust), shmipc-go (Go)

Simple one-directional log/event stream?
  └── Named Pipe (FIFO) or POSIX Message Queue
```

---

## Recommendation by Tier

### Tier 1 — Maximum Performance (50–300 ns latency)
**POSIX Shared Memory + lock-free MPMC ring buffer**
- Mount `emptyDir medium: Memory` at `/dev/shm`
- Use `shm_open` + `mmap` + atomic head/tail indices (or use a library)
- Libraries: iceoryx2 (C++/Rust), shmipc-go (Go), Disruptor (Java)
- 5–8M msg/sec, zero syscalls in data path, true zero-copy
- Significant implementation complexity

### Tier 2 — Best Simplicity/Performance Balance (1–2 µs latency) ⭐ Recommended
**Unix Domain Sockets (SOCK_SEQPACKET)**
- `emptyDir medium: Memory` volume mounted in both containers
- Standard socket API, every language, handles concurrency naturally
- 130k–192k msg/sec with single connection; scales with connection pool
- Industry standard: Envoy, Linkerd, CSI drivers all use UDS

### Tier 3 — Service-Style RPC (100–200 µs latency)
**gRPC over UDS**
- `emptyDir` volume + gRPC library
- Use streaming RPCs to amortize per-call overhead
- Best when schema evolution and multi-language support matter

---

## Sources

- [kamalmarhubi: Linux IPC latency data](https://kamalmarhubi.com/blog/2015/06/10/some-early-linux-ipc-latency-data/)
- [goldsborough/ipc-bench](https://github.com/goldsborough/ipc-bench)
- [howtech: Shared Memory vs Message Queues benchmarks](https://howtech.substack.com/p/ipc-mechanisms-shared-memory-vs-message)
- [3tilley: IPC in Rust - Ping Pong](https://3tilley.github.io/posts/simple-ipc-ping-pong/)
- [fwerner: gRPC for local IPC](https://www.mpi-hd.mpg.de/personalhomes/fwerner/research/2021/09/grpc-for-ipc/)
- [yanxurui: Benchmark TCP/IP vs UDS vs Named pipe](https://www.yanxurui.cc/posts/server/2023-11-28-benchmark-tcp-uds-namedpipe/)
- [athoscommerce: Speed up K8s Sidecar with Unix Sockets](https://athoscommerce.com/blog/speed-up-your-k8s-sidecar-with-unix-sockets/)
- [eclipse-iceoryx2 benchmarks](https://github.com/eclipse-iceoryx/iceoryx2/discussions/435)
- [cloudwego: shmipc-go introduction](https://www.cloudwego.io/blog/2023/04/04/introducing-shmipc-a-high-performance-inter-process-communication-library/)
- [ebpfchirp: eBPF SockMap acceleration](https://ebpfchirp.substack.com/p/optimizing-local-socket-communication)
- [linuxjournal: Lock-Free MPMC Ring Buffer](https://www.linuxjournal.com/content/lock-free-multi-producer-multi-consumer-queue-ring-buffer)
- [Kubernetes docs: Shared volumes](https://kubernetes.io/docs/tasks/access-application-cluster/communicate-containers-same-pod-shared-volume/)
- [Kubernetes docs: Share Process Namespace](https://kubernetes.io/docs/tasks/configure-pod-container/share-process-namespace/)
