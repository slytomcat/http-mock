package main

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
)

// MockWriter is mock writer for testing
type MockWriter struct {
	Written     chan []byte
	closed      bool
	lock        sync.Mutex
	flushCalled int64
	header      http.Header
	code        int
	writeFunc   func([]byte) (int, error)
	closeFunc   func() error
}

// NewMockWriter returns mock writer
func NewMockWriter() *MockWriter {
	return &MockWriter{
		Written: make(chan []byte, 1024),
		header:  http.Header{},
		closed:  false,
	}
}

// Write - io. writer implementation method
func (m *MockWriter) Write(data []byte) (int, error) {
	if m.writeFunc != nil {
		return m.writeFunc(data)
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		return 0, errors.New("writing to closed mock")
	}
	m.Written <- data
	return len(data), nil
}

// Close is io.WriteCloser interface implementation
func (m *MockWriter) Close() error {
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		return errors.New("closing already closed mock")
	}
	close(m.Written)
	m.closed = true
	return nil
}

// Flush is http.Flusher implementation
func (m *MockWriter) Flush() {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		panic("Flushing already closed mock")
	}
	atomic.AddInt64(&m.flushCalled, 1)
}

// Header is http.ResponseWriter implementation
func (m *MockWriter) Header() http.Header {
	return m.header
}

// WriteHeader is http.ResponseWriter implementation
func (m *MockWriter) WriteHeader(code int) {
	m.code = code
}

func (m *MockWriter) isClosed() bool {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.closed
}

func (m *MockWriter) flushCalls() int64 {
	return atomic.LoadInt64(&m.flushCalled)
}
