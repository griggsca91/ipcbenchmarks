// mqueue.go implements POSIX Message Queue IPC.
// This requires Linux — it will not compile on macOS.
//
//go:build linux

package method

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"unsafe"
)

/*
#cgo LDFLAGS: -lrt
#include <mqueue.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <errno.h>
#include <string.h>

typedef struct mq_attr mq_attr_t;

static mqd_t mq_open_rw(const char *name, int oflag, int maxmsg, int msgsize) {
    mq_attr_t attr;
    attr.mq_flags = 0;
    attr.mq_maxmsg = maxmsg;
    attr.mq_msgsize = msgsize;
    attr.mq_curmsgs = 0;
    return mq_open(name, oflag | O_CREAT, 0600, &attr);
}

static int mq_send_bytes(mqd_t mqd, const char *buf, size_t len) {
    return mq_send(mqd, buf, len, 0);
}

static ssize_t mq_receive_bytes(mqd_t mqd, char *buf, size_t len) {
    unsigned int prio;
    return mq_receive(mqd, buf, len, &prio);
}

static const char* last_error() {
    return strerror(errno);
}
*/
import "C"

const mqMaxMsg = 10

func mqReqName(clientID int) string {
	return fmt.Sprintf("/ipc-bench-req-%d", clientID)
}

func mqRespName(clientID int) string {
	return fmt.Sprintf("/ipc-bench-resp-%d", clientID)
}

func mqCheckMsgSizeMax(needed int) error {
	data, err := os.ReadFile("/proc/sys/fs/mqueue/msgsize_max")
	if err != nil {
		return fmt.Errorf("cannot read msgsize_max: %w", err)
	}
	max, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return fmt.Errorf("parse msgsize_max: %w", err)
	}
	if max < needed {
		return fmt.Errorf("msgsize_max=%d < %d; run: sudo sysctl -w fs.mqueue.msgsize_max=%d", max, needed, needed)
	}
	return nil
}

// mqPair holds one req/resp queue pair.
type mqPair struct {
	reqMQ  C.mqd_t
	respMQ C.mqd_t
}

// MQueueServer echoes payloads via POSIX message queues.
type MQueueServer struct {
	pairs []mqPair
	size  int
}

func (s *MQueueServer) Setup(cfg Config) error {
	wireSize := protoWireSize(cfg.Size)
	s.size = wireSize
	if err := mqCheckMsgSizeMax(wireSize); err != nil {
		return err
	}

	n := cfg.Parallel
	if n < 1 {
		n = 1
	}
	s.pairs = make([]mqPair, n)

	for i := 0; i < n; i++ {
		reqName := mqReqName(i)
		respName := mqRespName(i)

		// Clean up stale queues
		C.mq_unlink(C.CString(reqName))
		C.mq_unlink(C.CString(respName))

		s.pairs[i].reqMQ = C.mq_open_rw(C.CString(reqName), C.O_RDONLY, C.int(mqMaxMsg), C.int(wireSize))
		if s.pairs[i].reqMQ == -1 {
			return fmt.Errorf("mq_open req[%d]: %s", i, C.GoString(C.last_error()))
		}
		s.pairs[i].respMQ = C.mq_open_rw(C.CString(respName), C.O_WRONLY, C.int(mqMaxMsg), C.int(wireSize))
		if s.pairs[i].respMQ == -1 {
			return fmt.Errorf("mq_open resp[%d]: %s", i, C.GoString(C.last_error()))
		}
	}
	return nil
}

func (s *MQueueServer) servePair(ctx context.Context, p mqPair) error {
	buf := make([]byte, s.size)
	for {
		if ctx.Err() != nil {
			return nil
		}
		n := C.mq_receive_bytes(p.reqMQ, (*C.char)(unsafe.Pointer(&buf[0])), C.size_t(len(buf)))
		if n < 0 {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("mq_receive: %s", C.GoString(C.last_error()))
		}
		resp, err := echoPayload(buf[:n])
		if err != nil {
			return fmt.Errorf("mq server echo: %w", err)
		}
		rc := C.mq_send_bytes(p.respMQ, (*C.char)(unsafe.Pointer(&resp[0])), C.size_t(len(resp)))
		if rc < 0 {
			return fmt.Errorf("mq_send: %s", C.GoString(C.last_error()))
		}
	}
}

func (s *MQueueServer) Serve(ctx context.Context) error {
	if len(s.pairs) == 1 {
		return s.servePair(ctx, s.pairs[0])
	}

	var wg sync.WaitGroup
	errs := make([]error, len(s.pairs))
	for i, p := range s.pairs {
		wg.Add(1)
		go func(idx int, pair mqPair) {
			defer wg.Done()
			errs[idx] = s.servePair(ctx, pair)
		}(i, p)
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *MQueueServer) Teardown() error {
	for i, p := range s.pairs {
		C.mq_close(p.reqMQ)
		C.mq_close(p.respMQ)
		C.mq_unlink(C.CString(mqReqName(i)))
		C.mq_unlink(C.CString(mqRespName(i)))
	}
	return nil
}

// MQueueClient sends payloads via POSIX message queues.
type MQueueClient struct {
	reqMQ  C.mqd_t
	respMQ C.mqd_t
	buf    []byte
}

func (c *MQueueClient) Setup(cfg Config) error {
	wireSize := protoWireSize(cfg.Size)
	reqName := mqReqName(cfg.ClientID)
	respName := mqRespName(cfg.ClientID)
	c.reqMQ = C.mq_open_rw(C.CString(reqName), C.O_WRONLY, C.int(mqMaxMsg), C.int(wireSize))
	if c.reqMQ == -1 {
		return fmt.Errorf("mq_open req[%d]: %s", cfg.ClientID, C.GoString(C.last_error()))
	}
	c.respMQ = C.mq_open_rw(C.CString(respName), C.O_RDONLY, C.int(mqMaxMsg), C.int(wireSize))
	if c.respMQ == -1 {
		return fmt.Errorf("mq_open resp[%d]: %s", cfg.ClientID, C.GoString(C.last_error()))
	}
	c.buf = make([]byte, wireSize)
	return nil
}

func (c *MQueueClient) RoundTrip(payload []byte) ([]byte, error) {
	wire, err := marshalPayload(payload)
	if err != nil {
		return nil, fmt.Errorf("mq marshal: %w", err)
	}
	rc := C.mq_send_bytes(c.reqMQ, (*C.char)(unsafe.Pointer(&wire[0])), C.size_t(len(wire)))
	if rc < 0 {
		return nil, fmt.Errorf("mq_send: %s", C.GoString(C.last_error()))
	}
	n := C.mq_receive_bytes(c.respMQ, (*C.char)(unsafe.Pointer(&c.buf[0])), C.size_t(len(c.buf)))
	if n < 0 {
		return nil, fmt.Errorf("mq_receive: %s", C.GoString(C.last_error()))
	}
	return unmarshalPayload(c.buf[:n])
}

func (c *MQueueClient) Teardown() error {
	C.mq_close(c.reqMQ)
	C.mq_close(c.respMQ)
	return nil
}
