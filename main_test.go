package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"regexp"
	"strings"
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
	writeFunc   func([]byte) (int, error)
	closeFunc   func() error
}

func NewMockWriter() *MockWriter {
	return &MockWriter{
		Written: make(chan []byte, 1024),
		closed:  false,
	}
}

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

func (m *MockWriter) Flush() {
	m.lock.Lock()
	defer m.lock.Unlock()
	if m.closed {
		panic("Flushing already closed mock")
	}
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

func TestCachedWritCloser(t *testing.T) {
	mock := NewMockWriter()
	flushInterval = 5 * time.Millisecond
	wc := NewCachedWritCloserFlusher(mock, "", mock.Close, mock.Flush)
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
	require.False(t, mock.Closed())
	err := wc.Close()
	require.NoError(t, err)
	require.False(t, mock.Closed())
	n, err := wc.Write([]byte(""))
	require.Error(t, err)
	require.Zero(t, n)
	require.Error(t, wc.Close())
	time.Sleep(time.Millisecond)
	require.Greater(t, int64(13), atomic.LoadInt64(&mock.flushCalled))
	i := 0
	for d := range mock.Written {
		assert.Equal(t, messages[i%length], d)
		i++
	}
	require.Equal(t, 2*length, i)
	require.True(t, mock.Closed())
}

func TestCachedWritCloserErrors(t *testing.T) {
	tError := errors.New("test error")
	mock := NewMockWriter()
	wc := NewCachedWritCloserFlusher(mock, "", mock.Close, nil)
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
	require.False(t, mock.Closed())
	mock = NewMockWriter()
	wc = NewCachedWritCloserFlusher(mock, "", mock.Close, nil)
	mock.writeFunc = func([]byte) (int, error) {
		return 0, tError
	}
	n, err = wc.Write(data)
	require.NoError(t, err)
	require.Equal(t, 9, n)
	time.Sleep(time.Millisecond)
	require.True(t, mock.Closed())
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

func writeTempFile(data string) (string, error) {
	file, err := os.CreateTemp(os.TempDir(), "test*")
	if err != nil {
		return "", err
	}
	defer file.Close()
	_, err = file.Write([]byte(data))
	if err != nil {
		return "", err
	}
	return file.Name(), nil
}

func TestReadConfig(t *testing.T) {
	testCases := []struct {
		name     string
		path     string
		cfgBody  string
		errorStr string
	}{{
		name: "Ok",
		cfgBody: `[
    {
        "re": "/path\\?arg=val",
        "path": "sample_data/body_1.json",
        "headers": {
            "Connection": "keep-alive"
        }
    },
    {
        "re": "/wrong$",
		"body-re": "^$",
        "path": "/dev/null",
        "code": 400
    },
    {
        "re": "/stream$",
        "path": "sample_data/stream.data",
        "headers": {
            "Transfer-Encoding": "chunked",
            "Content-Type": "text/CSV; charset=utf-8"
        },
        "stream": true
    }]`,
	}, {
		name:     "wrong config path",
		path:     notExistingPath,
		errorStr: "config file opening/reading error: open /notExists/path: no such file or directory",
	}, {
		name:     "empty config",
		cfgBody:  `[]`,
		errorStr: "file %s contains no data",
	}, {
		name:     "bad json",
		cfgBody:  `[`,
		errorStr: "unmarshalling config error: unexpected end of JSON input",
	}, {
		name: "empty re",
		cfgBody: `[{
		"re": "",
		"path": "sample_data/body_1.json"
		}]`,
		errorStr: "record #0 doesn't contain mandatory values in fields 'path' and 're'",
	}, {
		name: "empty path",
		cfgBody: `[{
		"re": "^$",
		"path": ""
		}]`,
		errorStr: "record #0 doesn't contain mandatory values in fields 'path' and 're'",
	}, {
		name: "bad re",
		cfgBody: `[{
		"re": "\\3^",
		"path": "sample_data/body_1.json"
		}]`,
		errorStr: "compiling 're' field in record #0 error: error parsing regexp: invalid escape sequence: `\\3`",
	}, {
		name: "bad body re",
		cfgBody: `[{
		"re": "^$",
		"body-re": "\\3^",
		"path": "sample_data/body_1.json"
		}]`,
		errorStr: "compiling 'body-re' field in record #0 error: error parsing regexp: invalid escape sequence: `\\3`",
	}, {
		name: "path not exists",
		cfgBody: `[{
		"re": "^$",
		"path": "/notExists/path"
		}]`,
		errorStr: "file '/notExists/path' from 'path' of record #0 is not exists",
	}}

	for _, tCase := range testCases {
		t.Run(tCase.name, func(t *testing.T) {
			var path string
			var err error
			defer func() {
				filters = []filter{}
			}()
			if tCase.path != "" {
				path = tCase.path
			} else {
				path, err = writeTempFile(tCase.cfgBody)
				require.NoError(t, err)
			}
			err = readConfig(path)
			if tCase.errorStr != "" {
				if strings.Contains(tCase.errorStr, "%s") {
					tCase.errorStr = fmt.Sprintf(tCase.errorStr, path)
				}
				require.EqualError(t, err, tCase.errorStr)
			} else {
				require.NoError(t, err)
			}
		})
	}

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
	storeData(filePath, data, headers, url, code, "")
	require.Len(t, cfg, idx+1)
	c := cfg[idx]
	require.Equal(t, "^"+url+"$", c.Re)
	require.Equal(t, code, c.Code)
	require.EqualValues(t, headers, c.Headers)
	require.Equal(t, filePath, c.Path)
	require.Equal(t, false, c.Stream)
	storeData(notExistingPath, data, headers, url, code, "")
	require.Len(t, cfg, idx+1)
}
