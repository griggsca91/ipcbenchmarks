package harness

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"time"
)

// Results holds computed benchmark statistics.
type Results struct {
	Method     string        `json:"method"`
	Iterations int           `json:"iterations"`
	Parallel   int           `json:"parallel"`
	PayloadB   int           `json:"payload_bytes"`
	Min        time.Duration `json:"min_ns"`
	Max        time.Duration `json:"max_ns"`
	Mean       time.Duration `json:"mean_ns"`
	P50        time.Duration `json:"p50_ns"`
	P95        time.Duration `json:"p95_ns"`
	P99        time.Duration `json:"p99_ns"`
	Throughput float64       `json:"throughput_rps"`
	Elapsed    time.Duration `json:"elapsed_ns"`
	UserCPU    time.Duration `json:"user_cpu_ns"`
	SysCPU     time.Duration `json:"sys_cpu_ns"`
}

// ComputeResults calculates latency percentiles and throughput from raw measurements.
func ComputeResults(latencies []time.Duration, elapsed time.Duration, userCPU, sysCPU time.Duration) *Results {
	n := len(latencies)
	if n == 0 {
		return &Results{}
	}

	sorted := make([]time.Duration, n)
	copy(sorted, latencies)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	var sum time.Duration
	for _, l := range sorted {
		sum += l
	}

	return &Results{
		Iterations: n,
		Min:        sorted[0],
		Max:        sorted[n-1],
		Mean:       time.Duration(sum.Nanoseconds() / int64(n)),
		P50:        percentile(sorted, 50),
		P95:        percentile(sorted, 95),
		P99:        percentile(sorted, 99),
		Throughput: float64(n) / elapsed.Seconds(),
		Elapsed:    elapsed,
		UserCPU:    userCPU,
		SysCPU:     sysCPU,
	}
}

func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := p / 100.0 * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	frac := rank - float64(lower)
	return time.Duration(float64(sorted[lower])*(1-frac) + float64(sorted[upper])*frac)
}

// PrintResults outputs results as a formatted table to stdout.
func PrintResults(r *Results) {
	fmt.Printf("\n=== %s Benchmark Results ===\n", r.Method)
	fmt.Printf("  Iterations:  %d\n", r.Iterations)
	if r.Parallel > 1 {
		fmt.Printf("  Parallel:    %d goroutines\n", r.Parallel)
	}
	fmt.Printf("  Payload:     %d bytes\n", r.PayloadB)
	fmt.Printf("  Elapsed:     %v\n", r.Elapsed.Round(time.Millisecond))
	if r.Parallel > 1 {
		fmt.Printf("  Throughput:  %.0f round-trips/sec (aggregate)\n", r.Throughput)
	} else {
		fmt.Printf("  Throughput:  %.0f round-trips/sec\n", r.Throughput)
	}
	fmt.Println()
	fmt.Printf("  Latency (round-trip):\n")
	fmt.Printf("    Min:  %v\n", r.Min)
	fmt.Printf("    P50:  %v\n", r.P50)
	fmt.Printf("    P95:  %v\n", r.P95)
	fmt.Printf("    P99:  %v\n", r.P99)
	fmt.Printf("    Max:  %v\n", r.Max)
	fmt.Printf("    Mean: %v\n", r.Mean)
	fmt.Println()
	fmt.Printf("  CPU (main process):\n")
	fmt.Printf("    User: %v\n", r.UserCPU)
	fmt.Printf("    Sys:  %v\n", r.SysCPU)
	fmt.Println()
}

// PrintJSON outputs results as JSON to stdout.
func PrintJSON(r *Results) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}
