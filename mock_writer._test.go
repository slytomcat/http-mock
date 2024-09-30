package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMockWriter(t *testing.T) {
	m := NewMockWriter()
	n, err := m.Write([]byte("S"))
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Len(t, m.Written, 1)
	for len(m.Written) > 0 {
		<-m.Written
	}
	require.Len(t, m.Written, 0)
	for i := range 10 {
		n, err := m.Write(messages[i])
		assert.NoError(t, err)
		assert.Equal(t, 2, n)
	}
	require.Len(t, m.Written, 10)
	m.Close()
	i := 0
	for d := range m.Written {
		assert.Equal(t, messages[i], d, i)
		i++
	}
}

func TestMockWriterOther(t *testing.T) {
	testData := []byte("test")
	m := NewMockWriter()
	require.False(t, m.isClosed())
	require.NotPanics(t, m.Flush)
	require.Equal(t, int64(1), m.flushCalls())
	h := m.Header()
	require.NotNil(t, h)
	require.IsType(t, http.Header{}, h)
	m.WriteHeader(777)
	require.Equal(t, 777, m.code)
	err := m.Close()
	require.NoError(t, err)
	require.Panics(t, m.Flush)
	require.Error(t, m.Close())
	_, err = m.Write(testData)
	require.Error(t, err)
	cnt := 0
	m.closeFunc = func() error {
		cnt++
		return nil
	}
	require.NoError(t, m.Close())
	require.Equal(t, 1, cnt)
	written := []byte{}
	m.writeFunc = func(data []byte) (int, error) {
		written = data
		return len(data), nil
	}
	n, err := m.Write(testData)
	require.NoError(t, err)
	require.Equal(t, len(testData), n)
	require.Equal(t, testData, written)
}
