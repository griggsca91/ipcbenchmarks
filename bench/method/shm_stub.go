// shm_stub.go provides stub types on non-Linux platforms.
// shm_open-based shared memory is Linux-only.
//
//go:build !linux

package method

import (
	"context"
	"fmt"
	"runtime"
)

type SHMServer struct{}

func (s *SHMServer) Setup(cfg Config) error {
	return fmt.Errorf("shared memory ring buffer not supported on %s", runtime.GOOS)
}
func (s *SHMServer) Serve(ctx context.Context) error { return nil }
func (s *SHMServer) Teardown() error                 { return nil }

type SHMClient struct{}

func (c *SHMClient) Setup(cfg Config) error {
	return fmt.Errorf("shared memory ring buffer not supported on %s", runtime.GOOS)
}
func (c *SHMClient) RoundTrip(payload []byte) ([]byte, error) { return nil, nil }
func (c *SHMClient) Teardown() error                          { return nil }
