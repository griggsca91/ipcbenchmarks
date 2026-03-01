package method

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
)

// TCPServer echoes payloads over a TCP connection.
type TCPServer struct {
	listener net.Listener
}

func (s *TCPServer) Setup(cfg Config) error {
	ln, err := net.Listen("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("tcp listen: %w", err)
	}
	s.listener = ln
	return nil
}

func (s *TCPServer) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.listener.Close()
	}()

	for {
		conn, err := s.listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("tcp accept: %w", err)
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *TCPServer) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}

	header := make([]byte, 4)
	for {
		if ctx.Err() != nil {
			return
		}
		// Read length prefix
		if _, err := io.ReadFull(conn, header); err != nil {
			if ctx.Err() != nil || err == io.EOF {
				return
			}
			log.Printf("tcp server read header: %v", err)
			return
		}
		size := binary.BigEndian.Uint32(header)
		buf := make([]byte, size)
		if _, err := io.ReadFull(conn, buf); err != nil {
			log.Printf("tcp server read body: %v", err)
			return
		}

		// Unmarshal and re-marshal (protobuf echo)
		resp, err := echoPayload(buf)
		if err != nil {
			log.Printf("tcp server echo: %v", err)
			return
		}

		// Echo back with length prefix
		binary.BigEndian.PutUint32(header, uint32(len(resp)))
		if _, err := conn.Write(header); err != nil {
			log.Printf("tcp server write header: %v", err)
			return
		}
		if _, err := conn.Write(resp); err != nil {
			log.Printf("tcp server write body: %v", err)
			return
		}
	}
}

func (s *TCPServer) Teardown() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

// TCPClient sends payloads over a TCP connection.
type TCPClient struct {
	conn   net.Conn
	header []byte
	buf    []byte
}

func (c *TCPClient) Setup(cfg Config) error {
	conn, err := net.Dial("tcp", cfg.Address)
	if err != nil {
		return fmt.Errorf("tcp dial: %w", err)
	}
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetNoDelay(true)
	}
	c.conn = conn
	c.header = make([]byte, 4)
	c.buf = make([]byte, protoWireSize(cfg.Size))
	return nil
}

func (c *TCPClient) RoundTrip(payload []byte) ([]byte, error) {
	wire, err := marshalPayload(payload)
	if err != nil {
		return nil, fmt.Errorf("tcp marshal: %w", err)
	}

	// Send length + wire bytes
	binary.BigEndian.PutUint32(c.header, uint32(len(wire)))
	if _, err := c.conn.Write(c.header); err != nil {
		return nil, fmt.Errorf("tcp write header: %w", err)
	}
	if _, err := c.conn.Write(wire); err != nil {
		return nil, fmt.Errorf("tcp write body: %w", err)
	}

	// Read length + response
	if _, err := io.ReadFull(c.conn, c.header); err != nil {
		return nil, fmt.Errorf("tcp read header: %w", err)
	}
	size := binary.BigEndian.Uint32(c.header)
	if int(size) > len(c.buf) {
		c.buf = make([]byte, size)
	}
	if _, err := io.ReadFull(c.conn, c.buf[:size]); err != nil {
		return nil, fmt.Errorf("tcp read body: %w", err)
	}
	return unmarshalPayload(c.buf[:size])
}

func (c *TCPClient) Teardown() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
