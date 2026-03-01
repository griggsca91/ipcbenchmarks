package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"ipc-bench/harness"
	"ipc-bench/method"
)

func main() {
	role := flag.String("role", "", "Role: main or sidecar")
	meth := flag.String("method", "", "IPC method: tcp, uds, fifo, mqueue, grpc, shm")
	size := flag.Int("size", 5120, "Payload size in bytes")
	count := flag.Int("count", 100000, "Number of measured iterations")
	warmup := flag.Int("warmup", 10000, "Number of warmup iterations")
	parallel := flag.Int("parallel", 1, "Number of parallel clients (goroutines)")
	address := flag.String("address", "127.0.0.1:9000", "TCP address (host:port)")
	socketPath := flag.String("socket-path", "/run/ipc", "Base path for UDS/FIFO/gRPC sockets")
	jsonOut := flag.Bool("json", false, "Output results as JSON")
	flag.Parse()

	if *role == "" || *meth == "" {
		fmt.Fprintf(os.Stderr, "Usage: ipc-bench --role=<main|sidecar> --method=<tcp|uds|fifo|mqueue|grpc|shm>\n")
		os.Exit(1)
	}

	if *parallel < 1 {
		*parallel = 1
	}

	cfg := method.Config{
		SocketPath: *socketPath,
		Address:    *address,
		Size:       *size,
		Parallel:   *parallel,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	switch *role {
	case "sidecar":
		runSidecar(ctx, *meth, cfg)
	case "main":
		runMain(ctx, *meth, cfg, *size, *count, *warmup, *parallel, *jsonOut)
	default:
		log.Fatalf("unknown role: %s", *role)
	}
}

func newServer(name string) method.Server {
	switch name {
	case "tcp":
		return &method.TCPServer{}
	case "uds":
		return &method.UDSServer{}
	case "fifo":
		return &method.FIFOServer{}
	case "mqueue":
		return &method.MQueueServer{}
	case "grpc":
		return &method.GRPCServer{}
	case "shm":
		return &method.SHMServer{}
	default:
		log.Fatalf("unknown method: %s", name)
		return nil
	}
}

func newClient(name string) method.Client {
	switch name {
	case "tcp":
		return &method.TCPClient{}
	case "uds":
		return &method.UDSClient{}
	case "fifo":
		return &method.FIFOClient{}
	case "mqueue":
		return &method.MQueueClient{}
	case "grpc":
		return &method.GRPCClient{}
	case "shm":
		return &method.SHMClient{}
	default:
		log.Fatalf("unknown method: %s", name)
		return nil
	}
}

func runSidecar(ctx context.Context, name string, cfg method.Config) {
	srv := newServer(name)
	if err := srv.Setup(cfg); err != nil {
		log.Fatalf("server setup: %v", err)
	}
	defer srv.Teardown()

	log.Printf("sidecar: serving %s", name)
	if err := srv.Serve(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("server error: %v", err)
	}
	log.Printf("sidecar: shutting down")
}

func runMain(ctx context.Context, name string, cfg method.Config, size, count, warmup, parallel int, jsonOut bool) {
	// FIFO is inherently single-producer single-consumer
	if parallel > 1 && name == "fifo" {
		log.Printf("warning: FIFO does not support parallel mode, falling back to sequential (parallel=1)")
		parallel = 1
	}

	// Create N clients, each with its own ClientID
	clients := make([]method.Client, parallel)
	for i := 0; i < parallel; i++ {
		c := newClient(name)
		clientCfg := cfg
		clientCfg.ClientID = i

		// Retry client setup with backoff until sidecar is ready
		var err error
		for attempt := 0; attempt < 50; attempt++ {
			err = c.Setup(clientCfg)
			if err == nil {
				break
			}
			if ctx.Err() != nil {
				log.Fatalf("context cancelled waiting for sidecar: %v", ctx.Err())
			}
			time.Sleep(100 * time.Millisecond)
		}
		if err != nil {
			log.Fatalf("client %d setup failed after retries: %v", i, err)
		}
		clients[i] = c
	}
	defer func() {
		for _, c := range clients {
			c.Teardown()
		}
	}()

	benchCfg := harness.BenchConfig{
		Method: name,
		Size:   size,
		Count:  count,
		Warmup: warmup,
	}

	var results *harness.Results
	var err error

	if parallel > 1 {
		log.Printf("main: running %s benchmark (%d iterations, %d warmup, %d B payload, %d parallel)",
			name, count, warmup, size, parallel)
		results, err = harness.RunBenchmarkParallel(clients, benchCfg)
	} else {
		log.Printf("main: running %s benchmark (%d iterations, %d warmup, %d B payload)",
			name, count, warmup, size)
		results, err = harness.RunBenchmark(clients[0], benchCfg)
	}
	if err != nil {
		log.Fatalf("benchmark error: %v", err)
	}

	if jsonOut {
		if err := harness.PrintJSON(results); err != nil {
			log.Fatalf("json output: %v", err)
		}
	} else {
		harness.PrintResults(results)
	}
}
