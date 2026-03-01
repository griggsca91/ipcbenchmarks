// mqueue_stub.go provides stub types on non-Linux platforms.
// POSIX message queues are Linux-only.
//
//go:build !linux

package method

import (
	"context"
	"fmt"
	"runtime"
)

type MQueueServer struct{}

func (s *MQueueServer) Setup(cfg Config) error {
	return fmt.Errorf("POSIX message queues not supported on %s", runtime.GOOS)
}
func (s *MQueueServer) Serve(ctx context.Context) error { return nil }
func (s *MQueueServer) Teardown() error                 { return nil }

type MQueueClient struct{}

func (c *MQueueClient) Setup(cfg Config) error {
	return fmt.Errorf("POSIX message queues not supported on %s", runtime.GOOS)
}
func (c *MQueueClient) RoundTrip(payload []byte) ([]byte, error) { return nil, nil }
func (c *MQueueClient) Teardown() error                          { return nil }
