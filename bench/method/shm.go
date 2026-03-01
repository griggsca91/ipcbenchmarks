// shm.go implements shared memory IPC with a lock-free SPSC ring buffer.
// Requires Linux for shm_open.
//
//go:build linux

package method

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/unix"
)

/*
#include <sys/mman.h>
#include <sys/stat.h>
#include <fcntl.h>
#include <unistd.h>
#include <string.h>
#include <errno.h>

static int shm_open_create(const char *name, int size) {
    int fd = shm_open(name, O_CREAT | O_RDWR, 0600);
    if (fd < 0) return -1;
    if (ftruncate(fd, size) < 0) {
        close(fd);
        return -1;
    }
    return fd;
}

static int shm_open_existing(const char *name) {
    return shm_open(name, O_RDWR, 0);
}

static void shm_unlink_name(const char *name) {
    shm_unlink(name);
}

static const char* last_err() {
    return strerror(errno);
}
*/
import "C"

const (
	ringSlots    = 256
	cacheLinePad = 64
	slotOverhead = 4 // 4-byte length prefix per slot
)

func shmReqName(clientID int) string {
	return fmt.Sprintf("/ipc-bench-ring-req-%d", clientID)
}

func shmRespName(clientID int) string {
	return fmt.Sprintf("/ipc-bench-ring-resp-%d", clientID)
}

// ringHeader is at the start of the shared memory region.
// write_idx and read_idx are on separate cache lines to avoid false sharing.
type ringHeader struct {
	writeIdx uint64
	_pad1    [cacheLinePad - 8]byte
	readIdx  uint64
	_pad2    [cacheLinePad - 8]byte
}

const ringHeaderSize = int(unsafe.Sizeof(ringHeader{}))

func ringSlotSize(payloadSize int) int {
	return slotOverhead + payloadSize
}

func ringTotalSize(payloadSize int) int {
	return ringHeaderSize + ringSlots*ringSlotSize(payloadSize)
}

type ring struct {
	base     unsafe.Pointer
	slotSize int
	size     int
}

func (r *ring) header() *ringHeader {
	return (*ringHeader)(r.base)
}

func (r *ring) slotPtr(idx uint64) unsafe.Pointer {
	offset := ringHeaderSize + int(idx%ringSlots)*r.slotSize
	return unsafe.Add(r.base, offset)
}

func (r *ring) send(payload []byte) {
	h := r.header()
	wIdx := atomic.LoadUint64(&h.writeIdx)

	// Spin until slot is free (consumer has read it)
	spins := 0
	for {
		rIdx := atomic.LoadUint64(&h.readIdx)
		if wIdx-rIdx < ringSlots {
			break
		}
		spins++
		if spins > 100 {
			runtime.Gosched()
			spins = 0
		}
	}

	slot := r.slotPtr(wIdx)
	// Write length prefix
	binary.LittleEndian.PutUint32((*[4]byte)(slot)[:], uint32(len(payload)))
	// Copy payload
	dst := unsafe.Slice((*byte)(unsafe.Add(slot, slotOverhead)), len(payload))
	copy(dst, payload)

	// Release: make payload visible before advancing write index
	atomic.StoreUint64(&h.writeIdx, wIdx+1)
}

func (r *ring) recv(buf []byte) int {
	h := r.header()
	rIdx := atomic.LoadUint64(&h.readIdx)

	// Spin until data available
	spins := 0
	for {
		wIdx := atomic.LoadUint64(&h.writeIdx)
		if wIdx > rIdx {
			break
		}
		spins++
		if spins > 100 {
			runtime.Gosched()
			spins = 0
		}
	}

	slot := r.slotPtr(rIdx)
	n := int(binary.LittleEndian.Uint32((*[4]byte)(slot)[:]))
	src := unsafe.Slice((*byte)(unsafe.Add(slot, slotOverhead)), n)
	copy(buf[:n], src)

	// Release: advance read index
	atomic.StoreUint64(&h.readIdx, rIdx+1)
	return n
}

func mapRing(name string, payloadSize int, create bool) (*ring, error) {
	totalSize := ringTotalSize(payloadSize)
	cName := C.CString(name)

	var fd C.int
	if create {
		C.shm_unlink_name(cName)
		fd = C.shm_open_create(cName, C.int(totalSize))
	} else {
		fd = C.shm_open_existing(cName)
	}
	if fd < 0 {
		return nil, fmt.Errorf("shm_open %s: %s", name, C.GoString(C.last_err()))
	}
	defer C.close(fd)

	data, err := unix.Mmap(int(fd), 0, totalSize, unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap %s: %w", name, err)
	}

	r := &ring{
		base:     unsafe.Pointer(&data[0]),
		slotSize: ringSlotSize(payloadSize),
		size:     totalSize,
	}

	if create {
		// Zero out the header
		h := r.header()
		atomic.StoreUint64(&h.writeIdx, 0)
		atomic.StoreUint64(&h.readIdx, 0)
	}

	return r, nil
}

// shmPair holds one req/resp ring buffer pair.
type shmPair struct {
	reqRing  *ring
	respRing *ring
}

// SHMServer echoes payloads via shared memory ring buffers.
type SHMServer struct {
	pairs []shmPair
	size  int
}

func (s *SHMServer) Setup(cfg Config) error {
	s.size = cfg.Size
	n := cfg.Parallel
	if n < 1 {
		n = 1
	}
	s.pairs = make([]shmPair, n)

	for i := 0; i < n; i++ {
		var err error
		s.pairs[i].reqRing, err = mapRing(shmReqName(i), cfg.Size, true)
		if err != nil {
			return err
		}
		s.pairs[i].respRing, err = mapRing(shmRespName(i), cfg.Size, true)
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *SHMServer) servePair(ctx context.Context, p shmPair) error {
	buf := make([]byte, s.size)
	for {
		if ctx.Err() != nil {
			return nil
		}
		n := p.reqRing.recv(buf)
		p.respRing.send(buf[:n])
	}
}

func (s *SHMServer) Serve(ctx context.Context) error {
	if len(s.pairs) == 1 {
		return s.servePair(ctx, s.pairs[0])
	}

	var wg sync.WaitGroup
	errs := make([]error, len(s.pairs))
	for i, p := range s.pairs {
		wg.Add(1)
		go func(idx int, pair shmPair) {
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

func (s *SHMServer) Teardown() error {
	for i := range s.pairs {
		C.shm_unlink_name(C.CString(shmReqName(i)))
		C.shm_unlink_name(C.CString(shmRespName(i)))
	}
	return nil
}

// SHMClient sends payloads via shared memory ring buffers.
type SHMClient struct {
	reqRing  *ring
	respRing *ring
	buf      []byte
}

func (c *SHMClient) Setup(cfg Config) error {
	var err error
	c.reqRing, err = mapRing(shmReqName(cfg.ClientID), cfg.Size, false)
	if err != nil {
		return err
	}
	c.respRing, err = mapRing(shmRespName(cfg.ClientID), cfg.Size, false)
	if err != nil {
		return err
	}
	c.buf = make([]byte, cfg.Size)
	return nil
}

func (c *SHMClient) RoundTrip(payload []byte) ([]byte, error) {
	c.reqRing.send(payload)
	n := c.respRing.recv(c.buf)
	return c.buf[:n], nil
}

func (c *SHMClient) Teardown() error {
	return nil
}
