package main

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// CachedWriteFlusher is special io.WriteCloser for cache writing
type CachedWriteFlusher struct {
	input  chan []byte
	closed bool
	lock   sync.Mutex
}

// NewCachedWritCloserFlusher creates io.WriteCloser that pass data via channel buffer.
func NewCachedWritCloserFlusher(in io.Writer, logPrefix string, flushFunc func()) io.WriteCloser {
	input := make(chan []byte, 1024)
	c := &CachedWriteFlusher{
		input: input,
	}
	go func() {
		ticker := time.NewTicker(flushInterval)
		defer func() {
			if flushFunc != nil {
				ticker.Stop()
			}
		}()
		for {
			select {
			case data, ok := <-input:
				if !ok {
					return
				}
				if _, err := in.Write(data); err != nil {
					logger.Printf("%s: writing to underlying WC error: %s", logPrefix, err)
					c.lock.Lock()
					c.closed = true
					close(input)
					c.lock.Unlock()
					for range input {
					} // drain input
					if flushFunc != nil {
						ticker.Stop()
						for len(ticker.C) > 0 {
							<-ticker.C
						}
						flushFunc = nil
					}
					return
				}
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
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return fmt.Errorf("closing already closed CacheWriterFlusher")
	}
	c.closed = true
	close(c.input)

	return nil
}
