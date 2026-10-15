// Package ulid generates monotonic ULIDs (Crockford base32, 26 chars) for revisions, snapshots, sessions,
// leases, requests, updates and intents.
package ulid

import (
	"crypto/rand"
	"sync"
	"time"
)

const alphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	mu       sync.Mutex
	lastMs   int64
	lastRand [10]byte
)

// New returns a new ULID; ids made in the same millisecond are strictly increasing.
func New() string { return At(time.Now()) }

// At returns a ULID for the given time (monotonic within a millisecond).
func At(t time.Time) string {
	mu.Lock()
	defer mu.Unlock()
	ms := t.UnixMilli()
	if ms <= lastMs {
		ms = lastMs
		// increment the random part
		for i := 9; i >= 0; i-- {
			lastRand[i]++
			if lastRand[i] != 0 {
				break
			}
		}
	} else {
		lastMs = ms
		_, _ = rand.Read(lastRand[:])
		lastRand[0] &= 0x7f // leave room for increments
	}
	var b [16]byte
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (40 - 8*uint(i)))
	}
	copy(b[6:], lastRand[:])
	return encode(b)
}

func encode(b [16]byte) string {
	// 128 bits → 26 chars (first char holds 3 bits)
	var out [26]byte
	var n uint64
	hi := uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 | uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
	lo := uint64(b[8])<<56 | uint64(b[9])<<48 | uint64(b[10])<<40 | uint64(b[11])<<32 | uint64(b[12])<<24 | uint64(b[13])<<16 | uint64(b[14])<<8 | uint64(b[15])
	_ = n
	for i := 25; i >= 0; i-- {
		out[i] = alphabet[lo&31]
		lo = lo>>5 | (hi&31)<<59
		hi >>= 5
	}
	return string(out[:])
}

// Time returns the timestamp encoded in a ULID (zero time when invalid).
func Time(id string) time.Time {
	if len(id) != 26 {
		return time.Time{}
	}
	var ms int64
	for i := 0; i < 10; i++ {
		v := index(id[i])
		if v < 0 {
			return time.Time{}
		}
		ms = ms<<5 | int64(v)
	}
	return time.UnixMilli(ms)
}

func index(c byte) int {
	for i := 0; i < len(alphabet); i++ {
		if alphabet[i] == c {
			return i
		}
	}
	return -1
}
