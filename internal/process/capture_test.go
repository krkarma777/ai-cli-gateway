package process

import (
	"sync"
	"testing"
)

func TestCaptureCapsAndSignalsOnce(t *testing.T) {
	overflow := make(chan struct{}, 1)
	c := newCapture(4, overflow)
	if _, err := c.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if got := string(c.Bytes()); got != "abcd" {
		t.Fatalf("bytes=%q", got)
	}
	if c.Total() != 6 {
		t.Fatalf("total=%d", c.Total())
	}
	select {
	case <-overflow:
	default:
		t.Fatal("missing overflow signal")
	}

	if _, err := c.Write([]byte("ghij")); err != nil {
		t.Fatal(err)
	}
	if got := string(c.Bytes()); got != "abcd" {
		t.Fatalf("bytes after overflow=%q", got)
	}
	if c.Total() != 10 {
		t.Fatalf("total after overflow=%d", c.Total())
	}
	select {
	case <-overflow:
		t.Fatal("overflow signaled more than once")
	default:
	}
}

func TestCaptureConcurrentWritesRemainBounded(t *testing.T) {
	const (
		writers  = 32
		bytes    = 64
		capacity = 257
	)
	overflow := make(chan struct{}, 1)
	c := newCapture(capacity, overflow)
	var group sync.WaitGroup
	for range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			block := make([]byte, bytes)
			for i := range block {
				block[i] = 'x'
			}
			if _, err := c.Write(block); err != nil {
				t.Errorf("write: %v", err)
			}
		}()
	}
	group.Wait()
	if got := len(c.Bytes()); got != capacity {
		t.Fatalf("stored=%d", got)
	}
	if got := c.Total(); got != writers*bytes {
		t.Fatalf("total=%d", got)
	}
	if !c.Overflowed() {
		t.Fatal("overflow flag was not set")
	}
}
