package harness

import (
	"crypto/rand"
	"hash/crc32"
)

// GeneratePayload returns a random byte slice of the given size.
func GeneratePayload(size int) []byte {
	buf := make([]byte, size)
	if _, err := rand.Read(buf); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return buf
}

// Checksum returns the CRC-32 IEEE checksum of data.
func Checksum(data []byte) uint32 {
	return crc32.ChecksumIEEE(data)
}
