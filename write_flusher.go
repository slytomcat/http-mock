package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"
)

// CachedWriteFlusher is special io.WriteCloser for cache writing
type CachedWriteFlusher struct {
	ctx    context.Context
	input  chan []byte
	closed bool
	lock   sync.Mutex
}

// NewCachedWriteFlusher creates io.WriteCloser that pass data via channel buffer.
func NewCachedWriteFlusher(in io.Writer, logPrefix string, flushFunc func(), interval time.Duration) io.WriteCloser {
	input := make(chan []byte, 1024)
	ctx, cancel := context.WithCancel(context.Background())
	c := &CachedWriteFlusher{
		ctx:   ctx,
		input: input,
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer func() {
			if flushFunc != nil {
				ticker.Stop()
			}
			cancel()
		}()
		for {
			select {
			case data, ok := <-input:
				if !ok {
					return
				}
				if _, err := in.Write(data); err != nil {
					logger.Error(logPrefix, "desc", fmt.Sprintf("writing to underlying WC error: %s", err))
					c.lock.Lock()
					c.closed = true
					close(input)
					c.lock.Unlock()
					for range input {
					} // drain input
					if flushFunc != nil {
						ticker.Stop()
						flushFunc = nil
					}
					return
				}
				logger.Debug(logPrefix, "desc", "sent", "data", data)
			case <-ticker.C:
				flushFunc()
			}
		}
	}()
	return c
}

// Write chunk in to file as data line with delay and chunk data
func (c *CachedWriteFlusher) Write(data []byte) (int, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return 0, fmt.Errorf("writing to closed CacheWriterFlusher")
	}
	c.input <- data
	return len(data), nil
}

// Close underlying file
func (c *CachedWriteFlusher) Close() error {
	defer func() {
		<-c.ctx.Done() // wait for writing end
	}()
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return fmt.Errorf("closing already closed CacheWriterFlusher")
	}
	c.closed = true
	close(c.input)
	return nil
}
