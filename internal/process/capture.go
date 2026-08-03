package process

import (
	"bytes"
	"sync"
	"sync/atomic"
)

type capture struct {
	mu       sync.Mutex
	data     bytes.Buffer
	limit    int64
	total    int64
	overflow chan<- struct{}
	once     sync.Once
	exceeded atomic.Bool
}

func newCapture(limit int64, overflow chan<- struct{}) *capture {
	return &capture{
		limit:    limit,
		overflow: overflow,
	}
}

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.total += int64(len(p))
	remaining := c.limit - int64(c.data.Len())
	if remaining > 0 {
		count := len(p)
		if int64(count) > remaining {
			count = int(remaining)
		}
		_, _ = c.data.Write(p[:count])
	}
	if c.total > c.limit {
		c.exceeded.Store(true)
		c.once.Do(func() {
			select {
			case c.overflow <- struct{}{}:
			default:
			}
		})
	}
	return len(p), nil
}

func (c *capture) Bytes() []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	return bytes.Clone(c.data.Bytes())
}

func (c *capture) Total() int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.total
}

func (c *capture) Overflowed() bool {
	return c.exceeded.Load()
}
