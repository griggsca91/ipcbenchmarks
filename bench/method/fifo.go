package method

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const (
	fifoReqName  = "req.fifo"
	fifoRespName = "resp.fifo"
)

// FIFOServer echoes payloads over named pipes.
type FIFOServer struct {
	reqPath  string
	respPath string
	reqFile  *os.File
	respFile *os.File
}

func ensureFIFO(path string) error {
	// Remove and recreate to ensure clean state
	os.Remove(path)
	return unix.Mkfifo(path, 0600)
}

func (s *FIFOServer) Setup(cfg Config) error {
	s.reqPath = filepath.Join(cfg.SocketPath, fifoReqName)
	s.respPath = filepath.Join(cfg.SocketPath, fifoRespName)

	if err := ensureFIFO(s.reqPath); err != nil {
		return fmt.Errorf("mkfifo req: %w", err)
	}
	if err := ensureFIFO(s.respPath); err != nil {
		return fmt.Errorf("mkfifo resp: %w", err)
	}
	return nil
}

func (s *FIFOServer) Serve(ctx context.Context) error {
	// Open order: req for reading first (blocks until client opens for writing),
	// then resp for writing.
	var err error
	s.reqFile, err = os.OpenFile(s.reqPath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("fifo open req read: %w", err)
	}
	s.respFile, err = os.OpenFile(s.respPath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("fifo open resp write: %w", err)
	}

	header := make([]byte, 4)
	for {
		if ctx.Err() != nil {
			return nil
		}
		// Read length prefix
		if _, err := io.ReadFull(s.reqFile, header); err != nil {
			if ctx.Err() != nil || err == io.EOF {
				return nil
			}
			return fmt.Errorf("fifo read header: %w", err)
		}
		size := binary.BigEndian.Uint32(header)
		buf := make([]byte, size)
		if _, err := io.ReadFull(s.reqFile, buf); err != nil {
			return fmt.Errorf("fifo read body: %w", err)
		}

		// Unmarshal and re-marshal (protobuf echo)
		resp, err := echoPayload(buf)
		if err != nil {
			return fmt.Errorf("fifo server echo: %w", err)
		}

		// Echo back
		binary.BigEndian.PutUint32(header, uint32(len(resp)))
		if _, err := s.respFile.Write(header); err != nil {
			return fmt.Errorf("fifo write header: %w", err)
		}
		if _, err := s.respFile.Write(resp); err != nil {
			return fmt.Errorf("fifo write body: %w", err)
		}
	}
}

func (s *FIFOServer) Teardown() error {
	if s.reqFile != nil {
		s.reqFile.Close()
	}
	if s.respFile != nil {
		s.respFile.Close()
	}
	os.Remove(s.reqPath)
	os.Remove(s.respPath)
	return nil
}

// FIFOClient sends payloads over named pipes.
type FIFOClient struct {
	reqFile  *os.File
	respFile *os.File
	header   []byte
	buf      []byte
}

func (c *FIFOClient) Setup(cfg Config) error {
	reqPath := filepath.Join(cfg.SocketPath, fifoReqName)
	respPath := filepath.Join(cfg.SocketPath, fifoRespName)

	// Open order: req for writing first (blocks until server opens for reading),
	// then resp for reading. This prevents deadlock with server's open order.
	var err error
	c.reqFile, err = os.OpenFile(reqPath, os.O_WRONLY, 0)
	if err != nil {
		return fmt.Errorf("fifo open req write: %w", err)
	}
	c.respFile, err = os.OpenFile(respPath, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("fifo open resp read: %w", err)
	}

	c.header = make([]byte, 4)
	c.buf = make([]byte, protoWireSize(cfg.Size))
	return nil
}

func (c *FIFOClient) RoundTrip(payload []byte) ([]byte, error) {
	wire, err := marshalPayload(payload)
	if err != nil {
		return nil, fmt.Errorf("fifo marshal: %w", err)
	}

	// Send length + wire bytes
	binary.BigEndian.PutUint32(c.header, uint32(len(wire)))
	if _, err := c.reqFile.Write(c.header); err != nil {
		return nil, fmt.Errorf("fifo write header: %w", err)
	}
	if _, err := c.reqFile.Write(wire); err != nil {
		return nil, fmt.Errorf("fifo write body: %w", err)
	}

	// Read length + response
	if _, err := io.ReadFull(c.respFile, c.header); err != nil {
		return nil, fmt.Errorf("fifo read header: %w", err)
	}
	size := binary.BigEndian.Uint32(c.header)
	if int(size) > len(c.buf) {
		c.buf = make([]byte, size)
	}
	if _, err := io.ReadFull(c.respFile, c.buf[:size]); err != nil {
		return nil, fmt.Errorf("fifo read body: %w", err)
	}
	return unmarshalPayload(c.buf[:size])
}

func (c *FIFOClient) Teardown() error {
	if c.reqFile != nil {
		c.reqFile.Close()
	}
	if c.respFile != nil {
		c.respFile.Close()
	}
	return nil
}

