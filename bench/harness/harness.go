package harness

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"time"

	"ipc-bench/method"
)

// BenchConfig holds parameters for a benchmark run.
type BenchConfig struct {
	Method string
	Size   int
	Count  int
	Warmup int
}

func getRusage() syscall.Rusage {
	var ru syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &ru)
	return ru
}

func tvToD(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

// RunBenchmark executes the benchmark loop: warmup, then measured iterations.
func RunBenchmark(client method.Client, cfg BenchConfig) (*Results, error) {
	payload := GeneratePayload(cfg.Size)
	checksum := Checksum(payload)

	// Warmup
	for i := 0; i < cfg.Warmup; i++ {
		resp, err := client.RoundTrip(payload)
		if err != nil {
			return nil, fmt.Errorf("warmup iteration %d: %w", i, err)
		}
		if i == 0 && Checksum(resp) != checksum {
			return nil, fmt.Errorf("payload mismatch during warmup")
		}
	}

	// Force GC before measurement
	runtime.GC()

	// Measured iterations
	latencies := make([]time.Duration, cfg.Count)
	ruBefore := getRusage()
	start := time.Now()

	for i := 0; i < cfg.Count; i++ {
		t0 := time.Now()
		resp, err := client.RoundTrip(payload)
		latencies[i] = time.Since(t0)
		if err != nil {
			return nil, fmt.Errorf("iteration %d: %w", i, err)
		}
		// Validate first and last
		if (i == 0 || i == cfg.Count-1) && Checksum(resp) != checksum {
			return nil, fmt.Errorf("payload mismatch at iteration %d", i)
		}
	}

	elapsed := time.Since(start)
	ruAfter := getRusage()

	userCPU := tvToD(ruAfter.Utime) - tvToD(ruBefore.Utime)
	sysCPU := tvToD(ruAfter.Stime) - tvToD(ruBefore.Stime)

	results := ComputeResults(latencies, elapsed, userCPU, sysCPU)
	results.Method = cfg.Method
	results.Parallel = 1
	results.PayloadB = cfg.Size
	return results, nil
}

// RunBenchmarkParallel runs the benchmark with N concurrent clients.
// Each goroutine uses its own client and records latencies into its own slice.
func RunBenchmarkParallel(clients []method.Client, cfg BenchConfig) (*Results, error) {
	n := len(clients)
	payload := GeneratePayload(cfg.Size)
	checksum := Checksum(payload)

	// Warmup: each client does its share
	warmupPer := cfg.Warmup / n
	for ci, c := range clients {
		for i := 0; i < warmupPer; i++ {
			resp, err := c.RoundTrip(payload)
			if err != nil {
				return nil, fmt.Errorf("warmup client %d iteration %d: %w", ci, i, err)
			}
			if i == 0 && Checksum(resp) != checksum {
				return nil, fmt.Errorf("payload mismatch during warmup (client %d)", ci)
			}
		}
	}

	// Force GC before measurement
	runtime.GC()

	countPer := cfg.Count / n
	// Give remainder to last goroutine
	lastCount := countPer + (cfg.Count - countPer*n)

	// Pre-allocate per-goroutine latency slices
	perClient := make([][]time.Duration, n)
	for i := 0; i < n; i++ {
		c := countPer
		if i == n-1 {
			c = lastCount
		}
		perClient[i] = make([]time.Duration, c)
	}

	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)

	ruBefore := getRusage()
	start := time.Now()

	for gi := 0; gi < n; gi++ {
		go func(idx int) {
			defer wg.Done()
			c := clients[idx]
			lats := perClient[idx]
			// Each goroutine gets its own payload copy to avoid races
			p := make([]byte, len(payload))
			copy(p, payload)
			for i := range lats {
				t0 := time.Now()
				resp, err := c.RoundTrip(p)
				lats[i] = time.Since(t0)
				if err != nil {
					errs[idx] = fmt.Errorf("client %d iteration %d: %w", idx, i, err)
					return
				}
				// Validate first and last per goroutine
				if (i == 0 || i == len(lats)-1) && Checksum(resp) != checksum {
					errs[idx] = fmt.Errorf("payload mismatch at client %d iteration %d", idx, i)
					return
				}
			}
		}(gi)
	}

	wg.Wait()
	elapsed := time.Since(start)
	ruAfter := getRusage()

	// Check for errors
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}

	// Merge all latency slices
	total := 0
	for _, lats := range perClient {
		total += len(lats)
	}
	merged := make([]time.Duration, 0, total)
	for _, lats := range perClient {
		merged = append(merged, lats...)
	}

	userCPU := tvToD(ruAfter.Utime) - tvToD(ruBefore.Utime)
	sysCPU := tvToD(ruAfter.Stime) - tvToD(ruBefore.Stime)

	results := ComputeResults(merged, elapsed, userCPU, sysCPU)
	results.Method = cfg.Method
	results.Parallel = n
	results.PayloadB = cfg.Size
	return results, nil
}
