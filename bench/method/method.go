package method

import "context"

// Config holds connection parameters for IPC methods.
type Config struct {
	SocketPath string // base directory for UDS, FIFO, gRPC socket files
	Address    string // for TCP (host:port)
	Size       int    // payload size in bytes
	ClientID   int    // index of this client (0..Parallel-1), used by mqueue/shm for unique resource names
	Parallel   int    // total number of parallel clients; server uses this to create matching resources
}

// Server echoes payloads back to clients.
type Server interface {
	Setup(cfg Config) error
	Serve(ctx context.Context) error // blocks until ctx cancelled
	Teardown() error
}

// Client sends payloads and receives echoed responses.
type Client interface {
	Setup(cfg Config) error
	RoundTrip(payload []byte) ([]byte, error)
	Teardown() error
}
