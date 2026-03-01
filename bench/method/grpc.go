package method

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"

	pb "ipc-bench/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const grpcSocketName = "grpc.sock"

// GRPCServer implements the Bench gRPC service over a UDS or TCP.
type GRPCServer struct {
	Transport  string // "unix" (default) or "tcp"
	listener   net.Listener
	grpcServer *grpc.Server
	sockPath   string
}

type benchService struct {
	pb.UnimplementedBenchServer
}

func (s *benchService) Echo(_ context.Context, req *pb.Payload) (*pb.Payload, error) {
	return &pb.Payload{Data: req.Data}, nil
}

func (s *GRPCServer) Setup(cfg Config) error {
	var ln net.Listener
	var err error

	if s.Transport == "tcp" {
		ln, err = net.Listen("tcp", cfg.Address)
		if err != nil {
			return fmt.Errorf("grpc-tcp listen: %w", err)
		}
	} else {
		s.sockPath = filepath.Join(cfg.SocketPath, grpcSocketName)
		os.Remove(s.sockPath)
		ln, err = net.Listen("unix", s.sockPath)
		if err != nil {
			return fmt.Errorf("grpc listen: %w", err)
		}
	}

	s.listener = ln
	s.grpcServer = grpc.NewServer()
	pb.RegisterBenchServer(s.grpcServer, &benchService{})
	return nil
}

func (s *GRPCServer) Serve(ctx context.Context) error {
	go func() {
		<-ctx.Done()
		s.grpcServer.GracefulStop()
	}()
	return s.grpcServer.Serve(s.listener)
}

func (s *GRPCServer) Teardown() error {
	if s.grpcServer != nil {
		s.grpcServer.Stop()
	}
	if s.sockPath != "" {
		os.Remove(s.sockPath)
	}
	return nil
}

// GRPCClient calls the Bench gRPC service over a UDS or TCP.
type GRPCClient struct {
	Transport string // "unix" (default) or "tcp"
	conn      *grpc.ClientConn
	client    pb.BenchClient
}

func (c *GRPCClient) Setup(cfg Config) error {
	var target string
	if c.Transport == "tcp" {
		target = cfg.Address
	} else {
		sockPath := filepath.Join(cfg.SocketPath, grpcSocketName)
		target = "unix://" + sockPath
	}

	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		return fmt.Errorf("grpc dial: %w", err)
	}
	c.conn = conn
	c.client = pb.NewBenchClient(conn)
	return nil
}

func (c *GRPCClient) RoundTrip(payload []byte) ([]byte, error) {
	resp, err := c.client.Echo(context.Background(), &pb.Payload{Data: payload})
	if err != nil {
		return nil, fmt.Errorf("grpc echo: %w", err)
	}
	return resp.Data, nil
}

func (c *GRPCClient) Teardown() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}
