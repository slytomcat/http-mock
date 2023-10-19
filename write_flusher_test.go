package main

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var messages = [][]byte{[]byte("S0"), []byte("S1"), []byte("S2"), []byte("S3"), []byte("S4"), []byte("S5"), []byte("S6"), []byte("S7"), []byte("S8"), []byte("S9")}

func TestCachedWritCloser(t *testing.T) {
	mock := NewMockWriter()
	flushInterval := 5 * time.Millisecond
	wc := NewCachedWriteFlusher(mock, "", mock.Flush, flushInterval)
	length := len(messages)
	// slow
	for i := 0; i < length; i++ {
		time.Sleep(flushInterval)
		n, err := wc.Write(messages[i])
		assert.NoError(t, err, i)
		assert.Equal(t, 2, n, i)
	}
	// fast
	for i := 0; i < length; i++ {
		n, err := wc.Write(messages[i])
		assert.NoError(t, err, i)
		assert.Equal(t, 2, n, i)
	}
	time.Sleep(time.Millisecond)
	require.Len(t, mock.Written, 2*length)
	require.False(t, mock.isClosed())
	err := wc.Close()
	fc := atomic.LoadInt64(&mock.flushCalled)
	require.NoError(t, err)
	require.False(t, mock.isClosed())
	n, err := wc.Write([]byte(""))
	require.Error(t, err)
	require.Zero(t, n)
	require.Error(t, wc.Close())
	time.Sleep(flushInterval)
	require.Equal(t, fc, atomic.LoadInt64(&mock.flushCalled))
	i := 0
	for len(mock.Written) > 0 {
		assert.Equal(t, messages[i%length], <-mock.Written)
		i++
	}
	require.Equal(t, 2*length, i)
	require.False(t, mock.isClosed())
}

func TestCachedWritCloserErrors(t *testing.T) {
	tError := errors.New("test error")
	mock := NewMockWriter()
	wc := NewCachedWriteFlusher(mock, "", mock.Flush, time.Hour)
	mock.closeFunc = func() error {
		return tError
	}
	data := []byte("test data")
	n, err := wc.Write(data)
	require.NoError(t, err)
	require.Equal(t, 9, n)
	require.NoError(t, wc.Close())
	time.Sleep(time.Millisecond)
	require.Len(t, mock.Written, 1)
	require.Equal(t, data, <-mock.Written)
	require.False(t, mock.isClosed())
	mock = NewMockWriter()
	wc = NewCachedWriteFlusher(mock, "", mock.Flush, time.Hour)
	mock.writeFunc = func([]byte) (int, error) {
		return 0, tError
	}
	n, err = wc.Write(data)
	require.NoError(t, err)
	require.Equal(t, 9, n)
	time.Sleep(time.Millisecond)
	require.False(t, mock.isClosed())
}
