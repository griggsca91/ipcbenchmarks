package method

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/sys/unix"
)

const udsSocketName = "bench.sock"

// udsSocketType returns SOCK_SEQPACKET on Linux, SOCK_STREAM elsewhere.
// macOS does not support SOCK_SEQPACKET for AF_UNIX.
func udsSocketType() int {
	if runtime.GOOS == "linux" {
		return unix.SOCK_SEQPACKET
	}
	return unix.SOCK_STREAM
}

func udsIsStream() bool {
	return runtime.GOOS != "linux"
}

// UDSServer echoes payloads over a Unix domain socket.
// Uses SOCK_SEQPACKET on Linux, SOCK_STREAM with length framing elsewhere.
type UDSServer struct {
	fd       int
	sockPath string
}

func (s *UDSServer) Setup(cfg Config) error {
	s.sockPath = filepath.Join(cfg.SocketPath, udsSocketName)
	os.Remove(s.sockPath)

	fd, err := unix.Socket(unix.AF_UNIX, udsSocketType(), 0)
	if err != nil {
		return fmt.Errorf("uds socket: %w", err)
	}
	s.fd = fd

	addr := &unix.SockaddrUnix{Name: s.sockPath}
	if err := unix.Bind(fd, addr); err != nil {
		unix.Close(fd)
		return fmt.Errorf("uds bind: %w", err)
	}
	if err := unix.Listen(fd, 1); err != nil {
		unix.Close(fd)
		return fmt.Errorf("uds listen: %w", err)
	}
	return nil
}

func (s *UDSServer) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		unix.Close(s.fd)
	}()

	for {
		clientFd, _, err := unix.Accept(s.fd)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("uds accept: %w", err)
		}
		go s.handleClient(ctx, clientFd)
	}
}

func (s *UDSServer) handleClient(ctx context.Context, fd int) {
	defer unix.Close(fd)
	buf := make([]byte, 65536)
	header := make([]byte, 4)

	if udsIsStream() {
		// SOCK_STREAM: length-prefixed framing
		for {
			if ctx.Err() != nil {
				return
			}
			if err := recvFull(fd, header); err != nil {
				if ctx.Err() != nil {
					return
				}
				return
			}
			size := binary.BigEndian.Uint32(header)
			if err := recvFull(fd, buf[:size]); err != nil {
				log.Printf("uds read body: %v", err)
				return
			}
			resp, err := echoPayload(buf[:size])
			if err != nil {
				log.Printf("uds server echo: %v", err)
				return
			}
			binary.BigEndian.PutUint32(header, uint32(len(resp)))
			if err := sendFull(fd, header); err != nil {
				log.Printf("uds write header: %v", err)
				return
			}
			if err := sendFull(fd, resp); err != nil {
				log.Printf("uds write body: %v", err)
				return
			}
		}
	} else {
		// SOCK_SEQPACKET: message boundaries preserved
		for {
			if ctx.Err() != nil {
				return
			}
			n, _, err := unix.Recvfrom(fd, buf, 0)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Printf("uds recvfrom: %v", err)
				return
			}
			if n == 0 {
				return
			}
			resp, err := echoPayload(buf[:n])
			if err != nil {
				log.Printf("uds server echo: %v", err)
				return
			}
			if err := unix.Sendto(fd, resp, 0, nil); err != nil {
				log.Printf("uds sendto: %v", err)
				return
			}
		}
	}
}

func (s *UDSServer) Teardown() error {
	unix.Close(s.fd)
	os.Remove(s.sockPath)
	return nil
}

// UDSClient sends payloads over a Unix domain socket.
type UDSClient struct {
	fd     int
	buf    []byte
	header []byte
	stream bool
}

func (c *UDSClient) Setup(cfg Config) error {
	sockPath := filepath.Join(cfg.SocketPath, udsSocketName)

	fd, err := unix.Socket(unix.AF_UNIX, udsSocketType(), 0)
	if err != nil {
		return fmt.Errorf("uds client socket: %w", err)
	}

	addr := &unix.SockaddrUnix{Name: sockPath}
	if err := unix.Connect(fd, addr); err != nil {
		unix.Close(fd)
		return fmt.Errorf("uds connect: %w", err)
	}

	c.fd = fd
	c.buf = make([]byte, protoWireSize(cfg.Size))
	c.header = make([]byte, 4)
	c.stream = udsIsStream()
	return nil
}

func (c *UDSClient) RoundTrip(payload []byte) ([]byte, error) {
	wire, err := marshalPayload(payload)
	if err != nil {
		return nil, fmt.Errorf("uds marshal: %w", err)
	}

	if c.stream {
		// SOCK_STREAM: length-prefixed
		binary.BigEndian.PutUint32(c.header, uint32(len(wire)))
		if err := sendFull(c.fd, c.header); err != nil {
			return nil, fmt.Errorf("uds send header: %w", err)
		}
		if err := sendFull(c.fd, wire); err != nil {
			return nil, fmt.Errorf("uds send body: %w", err)
		}
		if err := recvFull(c.fd, c.header); err != nil {
			return nil, fmt.Errorf("uds recv header: %w", err)
		}
		size := binary.BigEndian.Uint32(c.header)
		if int(size) > len(c.buf) {
			c.buf = make([]byte, size)
		}
		if err := recvFull(c.fd, c.buf[:size]); err != nil {
			return nil, fmt.Errorf("uds recv body: %w", err)
		}
		return unmarshalPayload(c.buf[:size])
	}

	// SOCK_SEQPACKET: message boundaries preserved
	if err := unix.Sendto(c.fd, wire, 0, nil); err != nil {
		return nil, fmt.Errorf("uds send: %w", err)
	}
	n, _, err := unix.Recvfrom(c.fd, c.buf, 0)
	if err != nil {
		return nil, fmt.Errorf("uds recv: %w", err)
	}
	return unmarshalPayload(c.buf[:n])
}

func (c *UDSClient) Teardown() error {
	return unix.Close(c.fd)
}

// Helper functions for SOCK_STREAM framing
func recvFull(fd int, buf []byte) error {
	for len(buf) > 0 {
		n, err := unix.Read(fd, buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("unexpected EOF")
		}
		buf = buf[n:]
	}
	return nil
}

func sendFull(fd int, buf []byte) error {
	for len(buf) > 0 {
		n, err := unix.Write(fd, buf)
		if err != nil {
			return err
		}
		buf = buf[n:]
	}
	return nil
}
