package main

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const (
	notExistingPath = "/notExists/path"
)

func TestFormatChunk(t *testing.T) {
	data := []byte("some_data")
	delay := 123 * time.Millisecond
	require.Equal(t, []byte("                      123|some_data"), formatChink(delay, data))
}

type MockWriter struct {
	Written     chan []byte
	closed      bool
	lock        sync.Mutex
	flushCalled int64
	header      http.Header
}

func NewMockWriter() *MockWriter {
	return &MockWriter{
		Written: make(chan []byte, 1024),
		closed:  false,
	}
}

func (m *MockWriter) Write(data []byte) (int, error) {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		return 0, errors.New("writing to closed mock")
	}
	m.Written <- data
	return len(data), nil
}

func (m *MockWriter) Close() error {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		return errors.New("closing already closed mock")
	}
	close(m.Written)
	m.closed = true
	return nil
}

func (m *MockWriter) Flush() {
	atomic.AddInt64(&m.flushCalled, 1)
}

func (m *MockWriter) Closed() bool {
	m.lock.Lock()
	defer m.lock.Unlock()
	return m.closed
}

func (m *MockWriter) Header() http.Header {
	return http.Header{}
}

var messages = [][]byte{[]byte("S0"), []byte("S1"), []byte("S2"), []byte("S3"), []byte("S4"), []byte("S5"), []byte("S6"), []byte("S7"), []byte("S8"), []byte("S9")}

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
	wg := sync.WaitGroup{}
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func(i int) {
			defer wg.Done()
			time.Sleep(time.Millisecond * 10 * time.Duration(i))
			n, err := m.Write(messages[i])
			assert.NoError(t, err)
			assert.Equal(t, 2, n)
		}(i)
	}
	wg.Wait()
	require.Len(t, m.Written, 10)
	m.Close()
	i := 0
	for d := range m.Written {
		assert.Equal(t, messages[i], d, i)
		i++
	}
}

func TestCachedRecorder(t *testing.T) {
	mock := NewMockWriter()
	wc := NewCachedRecorder(mock)
	wg := sync.WaitGroup{}
	length := len(messages)
	wg.Add(length)
	for i := 0; i < length; i++ {
		go func(i int, w io.WriteCloser) {
			defer wg.Done()
			time.Sleep(time.Duration(i) * 10 * time.Millisecond)
			n, err := w.Write(messages[i])
			assert.NoError(t, err, i)
			assert.Equal(t, 2, n, i)
		}(i, wc)
	}
	wg.Wait()
	time.Sleep(time.Millisecond)
	require.Len(t, mock.Written, length)
	require.False(t, mock.Closed())
	err := wc.Close()
	require.NoError(t, err)
	require.False(t, mock.Closed())
	n, err := wc.Write([]byte(""))
	require.Error(t, err)
	require.Zero(t, n)
	require.Error(t, wc.Close())
	i := 0
	for d := range mock.Written {
		assert.Equal(t, messages[i], d)
		i++
	}
	require.Equal(t, length, i)
	require.True(t, mock.Closed())
}

func TestWriteCloserFromWriter(t *testing.T) {
	wc := NewMockWriter()
	wcw := NewWriteCloserFromWriter(wc)
	n, err := wcw.Write([]byte("data"))
	require.NoError(t, err)
	require.Equal(t, 4, n)
	require.False(t, wc.Closed())
	require.NoError(t, wcw.Close())
	require.False(t, wc.Closed())
}

func TestAsyncCounter(t *testing.T) {
	cnt := &asyncCounter{}
	length := 10
	results := make([]uint64, length, length)
	check := make(chan uint64, length)
	wg := sync.WaitGroup{}
	wg.Add(length)
	for i := 0; i < length; i++ {
		go func(i int) {
			defer wg.Done()
			v := cnt.Next()
			results[i] = v
			check <- v
		}(i)
	}
	wg.Wait()
	require.Len(t, check, length)
	close(check)
	m := make(map[uint64]bool, length)
	for n := range check {
		m[n] = true
	}
	require.Len(t, m, length)
	atomic.StoreUint64(&cnt.c, math.MaxUint64)
	require.Zero(t, cnt.Next())
}

func TestStartFlusher(t *testing.T) {
	wc := &MockWriter{}
	start := time.Now()
	stopper := startFlusher(wc)
	defer stopper()
	require.Eventually(t, func() bool { return atomic.LoadInt64(&wc.flushCalled) > 20 }, 400*time.Millisecond, time.Millisecond)
	elapsed := time.Since(start)
	require.InDelta(t, 200*time.Millisecond, elapsed, 3*float64(time.Millisecond))
}

func TestSendHeaders(t *testing.T) {
	header := http.Header{}
	values := map[string]string{"KEY1": "val1", "key2": "val2"}
	sendHeaders(header, values)
	for k, v := range values {
		require.Equal(t, v, header.Get(k))
	}
}

func loadCfgValues(t *testing.T, path string) []cfgValue {
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	values := make([]cfgValue, 3)
	err = json.Unmarshal(data, &values)
	require.NoError(t, err)
	return values
}

func compareConfig(t *testing.T, path string) {
	cfg := loadCfgValues(t, path)
	for i, v := range filters {
		c := cfg[i]
		compiled, err := regexp.Compile(c.Re)
		assert.NoError(t, err, i)
		assert.Equal(t, compiled.String(), v.re.String(), i)
		assert.Equal(t, c.Code, v.code, i)
		assert.Equal(t, c.Headers, v.headers, i)
		assert.Equal(t, c.Path, v.path, i)
		assert.Equal(t, c.Stream, v.stream, i)
	}
}

func writeTempFile(data []byte) (string, error) {
	file, err := os.CreateTemp(os.TempDir(), "test*")
	if err != nil {
		return "", err
	}
	defer file.Close()
	_, err = file.Write(data)
	if err != nil {
		return "", err
	}
	return file.Name(), nil
}

func TestReadConfig(t *testing.T) {
	path := "sample_data/config.json"
	err := readConfig(path)
	require.NoError(t, err)
	compareConfig(t, path)
	err = readConfig(notExistingPath)
	require.EqualError(t, err, "config file opening/reading error: open /notExists/path: no such file or directory")
	data, err := json.Marshal([]cfgValue{{
		Re:      "",
		Path:    path,
		Code:    0,
		Headers: map[string]string{},
		Stream:  false,
	}})
	require.NoError(t, err)
	path, err = writeTempFile(data)
	require.NoError(t, err)
	defer os.Remove(path)
	err = readConfig(path)
	require.EqualError(t, err, "record #0 doesn't contain mandatory values in fields 'path' and 're'")
	data, err = json.Marshal([]cfgValue{{
		Re:      "\\3^",
		Path:    path,
		Code:    0,
		Headers: map[string]string{},
		Stream:  false,
	}})
	require.NoError(t, err)
	path, err = writeTempFile(data)
	require.NoError(t, err)
	defer os.Remove(path)
	err = readConfig(path)
	require.EqualError(t, err, "compiling 're' field in record #0 error: error parsing regexp: invalid escape sequence: `\\3`")
	data, err = json.Marshal([]cfgValue{{
		Re:      "some",
		Path:    notExistingPath,
		Code:    0,
		Headers: map[string]string{},
		Stream:  false,
	}})
	require.NoError(t, err)
	path, err = writeTempFile(data)
	require.NoError(t, err)
	defer os.Remove(path)
	err = readConfig(path)
	require.EqualError(t, err, "file '/notExists/path' from 'path' of record #0 is not exists")
}

func TestStoreData(t *testing.T) {
	file, err := os.CreateTemp(os.TempDir(), "test*")
	require.NoError(t, err)
	filePath := file.Name()
	require.NoError(t, file.Close())
	idx := len(cfg)
	data := []byte("test data")
	url := "/some/url"
	code := 777
	headers := map[string]string{"key": "val"}
	storeData(filePath, data, headers, url, code)
	require.Len(t, cfg, idx+1)
	c := cfg[idx]
	require.Equal(t, "^"+url+"$", c.Re)
	require.Equal(t, code, c.Code)
	require.EqualValues(t, headers, c.Headers)
	require.Equal(t, filePath, c.Path)
	require.Equal(t, false, c.Stream)
	storeData(notExistingPath, data, headers, url, code)
	require.Len(t, cfg, idx+1)
}
