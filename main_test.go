package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
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
	code        int
	writeFunc   func([]byte) (int, error)
	closeFunc   func() error
}

func NewMockWriter() *MockWriter {
	return &MockWriter{
		Written: make(chan []byte, 1024),
		header:  http.Header{},
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
	return m.header
}

func (m *MockWriter) WriteHeader(code int) {
	m.code = code
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
	wc := NewCachedWritCloserFlusher(mock, "", mock.Flush)
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
	wc := NewCachedWritCloserFlusher(mock, "", mock.Flush)
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
	wc = NewCachedWritCloserFlusher(mock, "", mock.Flush)
	mock.writeFunc = func([]byte) (int, error) {
		return 0, tError
	}
	n, err = wc.Write(data)
	require.NoError(t, err)
	require.Equal(t, 9, n)
	time.Sleep(time.Millisecond)
	require.True(t, mock.Closed())
}

// func TestAsyncCounter(t *testing.T) {
// 	cnt := &asyncCounter{}
// 	length := 10
// 	results := make([]uint64, length, length)
// 	check := make(chan uint64, length)
// 	wg := sync.WaitGroup{}
// 	wg.Add(length)
// 	for i := 0; i < length; i++ {
// 		go func(i int) {
// 			defer wg.Done()
// 			v := cnt.Next()
// 			results[i] = v
// 			check <- v
// 		}(i)
// 	}
// 	wg.Wait()
// 	require.Len(t, check, length)
// 	close(check)
// 	m := make(map[uint64]bool, length)
// 	for n := range check {
// 		m[n] = true
// 	}
// 	require.Len(t, m, length)
// 	atomic.StoreUint64(&cnt.c, math.MaxUint64)
// 	require.Zero(t, cnt.Next())
// }

func TestSendHeaders(t *testing.T) {
	header := http.Header{}
	values := map[string]string{"KEY1": "val1", "key2": "val2"}
	sendHeaders(header, values)
	for k, v := range values {
		require.Equal(t, v, header.Get(k))
	}
}

// func loadCfgValues(t *testing.T, path string) []cfgValue {
// 	data, err := os.ReadFile(path)
// 	require.NoError(t, err)
// 	values := make([]cfgValue, 3)
// 	err = json.Unmarshal(data, &values)
// 	require.NoError(t, err)
// 	return values
// }

func writeTempFile(tempDit, data string) (string, error) {
	file, err := os.CreateTemp(tempDit, "test*")
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

// func TestReadConfig(t *testing.T) {
// 	testCases := []struct {
// 		name     string
// 		path     string
// 		cfgBody  string
// 		errorStr string
// 	}{{
// 		name: "Ok",
// 		cfgBody: `[
//     {
//         "re": "/path\\?arg=val",
//         "path": "sample_data/body_1.json",
//         "headers": {
//             "Connection": "keep-alive"
//         }
//     },
//     {
//         "re": "/wrong$",
// 		"body-re": "^$",
//         "path": "/dev/null",
//         "code": 400
//     },
//     {
//         "re": "/stream$",
//         "path": "sample_data/stream.data",
//         "headers": {
//             "Transfer-Encoding": "chunked",
//             "Content-Type": "text/CSV; charset=utf-8"
//         },
//         "stream": true
//     }]`,
// 	}, {
// 		name:     "wrong config path",
// 		path:     notExistingPath,
// 		errorStr: "config file opening/reading error: open /notExists/path: no such file or directory",
// 	}, {
// 		name:     "empty config",
// 		cfgBody:  `[]`,
// 		errorStr: "file %s contains no data",
// 	}, {
// 		name:     "bad json",
// 		cfgBody:  `[`,
// 		errorStr: "decoding config error: unexpected end of JSON input",
// 	}, {
// 		name: "empty re",
// 		cfgBody: `[{
// 		"re": "",
// 		"path": "sample_data/body_1.json"
// 		}]`,
// 		errorStr: "record #0 doesn't contain mandatory values in fields 'path' and 're'",
// 	}, {
// 		name: "empty path",
// 		cfgBody: `[{
// 		"re": "^$",
// 		"path": ""
// 		}]`,
// 		errorStr: "record #0 doesn't contain mandatory values in fields 'path' and 're'",
// 	}, {
// 		name: "bad re",
// 		cfgBody: `[{
// 		"re": "\\3^",
// 		"path": "sample_data/body_1.json"
// 		}]`,
// 		errorStr: "compiling 're' field in record #0 error: error parsing regexp: invalid escape sequence: `\\3`",
// 	}, {
// 		name: "bad body re",
// 		cfgBody: `[{
// 		"re": "^$",
// 		"body-re": "\\3^",
// 		"path": "sample_data/body_1.json"
// 		}]`,
// 		errorStr: "compiling 'body-re' field in record #0 error: error parsing regexp: invalid escape sequence: `\\3`",
// 	}, {
// 		name: "path not exists",
// 		cfgBody: `[{
// 		"re": "^$",
// 		"path": "/notExists/path"
// 		}]`,
// 		errorStr: "file '/notExists/path' from 'path' of record #0 is not exists",
// 	}}

// 	for _, tCase := range testCases {
// 		t.Run(tCase.name, func(t *testing.T) {
// 			var path string
// 			var err error
// 			tempDir := t.TempDir()
// 			defer func() {
// 				filters = []filter{}
// 			}()
// 			if tCase.path != "" {
// 				path = tCase.path
// 			} else {
// 				path, err = writeTempFile(tempDir, tCase.cfgBody)
// 				require.NoError(t, err)
// 			}
// 			err = readConfig(path)
// 			if tCase.errorStr != "" {
// 				if strings.Contains(tCase.errorStr, "%s") {
// 					tCase.errorStr = fmt.Sprintf(tCase.errorStr, path)
// 				}
// 				require.EqualError(t, err, tCase.errorStr)
// 			} else {
// 				require.NoError(t, err)
// 			}
// 		})
// 	}
// }

// func TestStoreData(t *testing.T) {
// 	file, err := os.CreateTemp(os.TempDir(), "test*")
// 	require.NoError(t, err)
// 	filePath := file.Name()
// 	require.NoError(t, file.Close())
// 	idx := len(cfg)
// 	data := []byte("test data")
// 	url := "/some/url"
// 	code := 777
// 	headers := map[string]string{"key": "val"}
// 	storeData(filePath, data, headers, url, code, "")
// 	require.Len(t, cfg, idx+1)
// 	c := cfg[idx]
// 	require.Equal(t, "^"+url+"$", c.Re)
// 	require.Equal(t, code, c.Code)
// 	require.EqualValues(t, headers, c.Headers)
// 	require.Equal(t, filePath, c.Path)
// 	require.Equal(t, false, c.Stream)
// 	storeData(notExistingPath, data, headers, url, code, "")
// 	require.Len(t, cfg, idx+1)
// }

type testFile struct {
	name    string
	content string
}

// func TestHandlers(t *testing.T) {
// 	testCases := []struct {
// 		name      string            // test name
// 		handler   bool              // True mock, False for proxy
// 		config    string            // config data
// 		files     []testFile        // responses files
// 		forward   string            // forward URL
// 		method    string            // request method
// 		url       string            // request url
// 		body      string            // request body
// 		code      int               // request status code
// 		headers   map[string]string // request headers
// 		tEncoding bool              // add `Transfer-Encoding: chunked` to request header
// 		chunked   bool              // chunked means that Transfer-Encoding: chunked must exists in the response and response have to be chunked
// 		expCode   int               // expected code
// 		expResp   string            // expected response data
// 		expChunks []string          // expecter chunked data
// 		recAll    bool              // receive all chunked data
// 		expHeader map[string]string // header of received response
// 	}{
// 		{
// 			name:      "GET 402 response",
// 			handler:   true, // mock mode
// 			method:    http.MethodGet,
// 			url:       "/wrong",
// 			config:    `[{"re": "^/wrong$", "path": "/dev/null", "code": 402}]`,
// 			expCode:   402,
// 			expHeader: map[string]string{"Content-Length": "0"},
// 			recAll:    true,
// 		},
// 		{
// 			name:      "GET 404 response",
// 			handler:   true, // mock mode
// 			method:    http.MethodGet,
// 			url:       "/not/exists",
// 			config:    `[{"re": "^/wrong$", "path": "/dev/null", "code": 402}]`,
// 			expCode:   404,
// 			expHeader: map[string]string{"Content-Length": "0"},
// 			recAll:    true,
// 		},
// 		{
// 			name:    "GET 200 response w body",
// 			handler: true, // mock mode
// 			method:  http.MethodGet,
// 			url:     "/exists",
// 			headers: map[string]string{"Accept-Encoding": "gzip/deflate"},
// 			config:  `[{"re": "^/exists$", "path": "{dir}/resp1", "code": 200}]`,
// 			files: []testFile{{
// 				name:    "%s/resp1",
// 				content: "some data",
// 			}},
// 			expCode:   200,
// 			expResp:   "some data",
// 			expHeader: map[string]string{"Content-Length": "9"},
// 			recAll:    true,
// 		},
// 		{
// 			name:    "POST 200 response w body re",
// 			handler: true, // mock mode
// 			method:  http.MethodPost,
// 			url:     "/",
// 			body:    `{"this": "body"}`,
// 			headers: map[string]string{"Connection": "close"},
// 			config:  `[{"re": "^/$", "body-re": ".*body.*", "path": "{dir}/resp1", "code": 200}]`,
// 			files: []testFile{{
// 				name:    "%s/resp1",
// 				content: "some data",
// 			}},
// 			expCode:   200,
// 			expResp:   "some data",
// 			expHeader: map[string]string{"Content-Length": "9"},
// 			recAll:    true,
// 		},
// 		{
// 			name:    "POST 200 response w body hash",
// 			handler: true, // mock mode
// 			method:  http.MethodPost,
// 			url:     "/",
// 			body:    `{"this": "body"}`,
// 			config:  `[{"re": "^/$", "body-hash": "988A1BDA6F42A960A690411E7C51BC4DD0034C3F562ABE0508F567BA0AD7C103", "path": "{dir}/resp1", "code": 200}]`,
// 			files: []testFile{{
// 				name:    "%s/resp1",
// 				content: "some data",
// 			}},
// 			expCode:   200,
// 			expResp:   "some data",
// 			expHeader: map[string]string{"Content-Length": "9"},
// 			recAll:    true,
// 		},
// 		{
// 			name:    "GET 200 ignore Transfer-Encoding",
// 			handler: true, // mock mode
// 			method:  http.MethodGet,
// 			url:     "/exists",
// 			config:  `[{"re": "^/exists$", "path": "{dir}/resp1", "code": 200, "headers":{"Transfer-Encoding": "chunked"}}]`,
// 			files: []testFile{{
// 				name:    "%s/resp1",
// 				content: "some data",
// 			}},
// 			expCode:   200,
// 			expResp:   "some data",
// 			expHeader: map[string]string{"Content-Length": "9"},
// 			recAll:    true,
// 		},
// 		{
// 			name:    "get 200 chunked response",
// 			handler: true, // mock mode
// 			method:  http.MethodGet,
// 			url:     "/exists",
// 			chunked: true,
// 			config:  `[{"re": "^/exists$", "path": "{dir}/resp1", "code": 200, "stream": true}]`,
// 			files: []testFile{{
// 				name: "%s/resp1",
// 				content: `                       20|data line #1
//                        10|data line #2
//                        10|data line #3
//                        10|data line #4`,
// 			}},
// 			expCode: 200,
// 			expChunks: []string{
// 				"data line #1\n",
// 				"data line #2\n",
// 				"data line #3\n",
// 				"data line #4\n",
// 			},
// 			expHeader: map[string]string{"Transfer-Encoding": "chunked"},
// 			recAll:    true,
// 		},
// 		{
// 			name:    "get 200 chunked response not all",
// 			handler: true, // mock mode
// 			method:  http.MethodGet,
// 			url:     "/exists",
// 			chunked: true,
// 			config:  `[{"re": "^/exists$", "path": "{dir}/resp1", "code": 200, "stream": true}]`,
// 			files: []testFile{{
// 				name: "%s/resp1",
// 				content: `                       20|data line #1
//                        10|data line #2
//                        10|data line #3
//                        10|data line #4`,
// 			}},
// 			expCode: 200,
// 			expChunks: []string{
// 				"data line #1\n",
// 				"data line #2\n",
// 				"data line #3\n",
// 			},
// 			expHeader: map[string]string{"Transfer-Encoding": "chunked"},
// 			recAll:    false,
// 		},
// 		{
// 			name:      "GET 200 w body",
// 			handler:   false, // proxy mode
// 			method:    http.MethodGet,
// 			url:       "/path",
// 			code:      200,
// 			headers:   map[string]string{"Connection": "close", "Accept-Encoding": "gzip/deflate"},
// 			chunked:   false,
// 			expCode:   200,
// 			expResp:   "data",
// 			expHeader: map[string]string{"Content-Length": "4", "Content-Type": "text/plain; charset=utf-8"},
// 			files: []testFile{{
// 				name:    "%s/path_response_1.raw",
// 				content: "data",
// 			}},
// 			config: `[{"re": "^/path$", "path": "{dir}/path_response_1.raw", "code": 200]`,
// 		},
// 		{
// 			name:      "GET 404 wo body",
// 			handler:   false, // proxy mode
// 			method:    http.MethodGet,
// 			url:       "/not/exists",
// 			chunked:   false,
// 			code:      404,
// 			expCode:   404,
// 			expHeader: map[string]string{"Content-Length": "0", "Connection": "close"},
// 			files: []testFile{{
// 				name:    "%s/not_exists_response_1.raw",
// 				content: "",
// 			}},
// 		},
// 		{
// 			name:    "get 200 chunked response",
// 			handler: false, // proxy mode
// 			method:  http.MethodGet,
// 			url:     "/exists",
// 			chunked: true,
// 			files: []testFile{{
// 				name: "%s/exists_response_1.raw",
// 			}},
// 			code:    200,
// 			expCode: 200,
// 			expChunks: []string{
// 				"data line #1\n",
// 				"data line #2\n",
// 				"data line #3\n",
// 				"data line #4\n",
// 			},
// 			expHeader: map[string]string{"Transfer-Encoding": "chunked"},
// 			recAll:    true,
// 		},
// 		{
// 			name:    "get 200 chunked response not all",
// 			handler: false, // proxy mode
// 			method:  http.MethodGet,
// 			url:     "/exists",
// 			code:    200,
// 			chunked: true,
// 			files: []testFile{{
// 				name: "%s/exists_response_1.raw",
// 			}},
// 			expCode: 200,
// 			expChunks: []string{
// 				"data line #1\n",
// 				"data line #2\n",
// 				"data line #3\n",
// 			},
// 			expHeader: map[string]string{"Transfer-Encoding": "chunked"},
// 			recAll:    false,
// 		},
// 	}
// 	cleanUp()
// 	for _, tc := range testCases {
// 		testName := tc.name
// 		if tc.handler {
// 			testName = "MockHandler " + testName
// 		} else {
// 			testName = "ProxyHandler " + testName
// 		}
// 		t.Run(testName, func(t *testing.T) {
// 			defer cleanUp()
// 			dataDirName = t.TempDir()
// 			w := NewMockWriter()
// 			r, err := http.NewRequest(tc.method, tc.url, bytes.NewReader([]byte(tc.body)))
// 			if tc.chunked {
// 				r.TransferEncoding = []string{"chunked"}
// 			}
// 			for k, v := range tc.headers {
// 				r.Header.Add(k, v)
// 			}
// 			if tc.tEncoding {
// 				r.TransferEncoding = []string{"chunked"}
// 			}
// 			require.NoError(t, err)
// 			if tc.handler { // mock
// 				if len(tc.config) > 0 {
// 					for _, f := range tc.files {
// 						err := os.WriteFile(fmt.Sprintf(f.name, dataDirName), []byte(f.content), 0644)
// 						require.NoError(t, err)
// 					}
// 					cfgPath := path.Join(dataDirName, "config.json")
// 					config := strings.ReplaceAll(tc.config, "{dir}", dataDirName)
// 					err := os.WriteFile(cfgPath, []byte(config), 0644)
// 					require.NoError(t, err)
// 					err = readConfig(cfgPath)
// 					require.NoError(t, err)
// 				}
// 				MockHandler(w, r)
// 				assert.Equal(t, tc.expCode, w.code)
// 				headers := map[string]string{}
// 				chunked := false
// 				for k, v := range w.header {
// 					if k == "Transfer-Encoding" && v[0] == "chunked" {
// 						chunked = true
// 					}
// 					headers[k] = strings.Join(v, "/")
// 				}
// 				assert.EqualValues(t, tc.expHeader, headers)
// 				assert.Equal(t, tc.chunked, chunked)
// 				if chunked {
// 					for _, ch := range tc.expChunks {
// 						received, f := channelReader(w.Written)
// 						assert.Eventually(t, f, 50*time.Millisecond, 5*time.Microsecond)
// 						assert.Equal(t, ch, fmt.Sprintf("%s", *received))
// 					}
// 				} else {
// 					if len(tc.expResp) > 0 {
// 						received, f := channelReader(w.Written)
// 						assert.Eventually(t, f, 50*time.Millisecond, 5*time.Microsecond)
// 						assert.Equal(t, tc.expResp, fmt.Sprintf("%s", *received))
// 					} else {
// 						if len(w.Written) > 0 {
// 							data := <-w.Written
// 							assert.Equal(t, "", fmt.Sprint(data))
// 						}
// 					}
// 				}
// 				if tc.recAll {
// 					assert.Len(t, w.Written, 0)
// 				}
// 			} else { // proxy
// 				defer cleanUp()
// 				dataDirName = t.TempDir()
// 				handler := &testHandler{}
// 				server := httptest.NewServer(handler)
// 				client = server.Client()
// 				forwardURL = server.URL
// 				if tc.chunked {
// 					handler.handleFunc = func(w http.ResponseWriter, r *http.Request) {
// 						w.WriteHeader(tc.expCode)
// 						for k, v := range tc.expHeader {
// 							w.Header().Add(k, v)
// 						}
// 						flush := w.(http.Flusher).Flush
// 						for _, chunk := range tc.expChunks {
// 							time.Sleep(5 * time.Millisecond)
// 							w.Write([]byte(chunk))
// 							flush()
// 						}
// 					}
// 				} else {
// 					handler.code = tc.expCode
// 					handler.response = []byte(tc.expResp)
// 					handler.headers = tc.expHeader
// 				}
// 				ProxyHandler(w, r)
// 				assert.Equal(t, tc.expCode, w.code)
// 				time.Sleep(5 * time.Millisecond)
// 				fn := ""
// 				for _, f := range tc.files {
// 					fn = fmt.Sprintf(f.name, dataDirName)
// 					data, err := os.ReadFile(fn)
// 					assert.NoError(t, err)
// 					if tc.chunked {
// 						scanner := bufio.NewScanner(bytes.NewReader(data))
// 						i := 0
// 						for scanner.Scan() {
// 							assert.Equal(t, tc.expChunks[i], scanner.Text()[26:]+"\n")
// 							i++
// 						}
// 					} else {
// 						assert.Equal(t, f.content, fmt.Sprintf("%s", data))
// 					}
// 				}
// 				headers := map[string]string{}
// 				chunked := false
// 				for k, v := range w.header {
// 					if k == "Transfer-Encoding" && v[0] == "chunked" {
// 						chunked = true
// 					}
// 					headers[k] = strings.Join(v, "/")
// 				}
// 				assert.Equal(t, tc.chunked, chunked)
// 				cfgLock.Lock()
// 				assert.Len(t, cfg, 1)
// 				c := cfg[0]
// 				cfgLock.Unlock()
// 				assert.EqualValues(t, headers, c.Headers)
// 				assert.Equal(t, fmt.Sprintf("^%s$", tc.url), c.Re)
// 				assert.Equal(t, fn, c.Path)
// 				assert.Equal(t, "^$", c.BodyRe)
// 				assert.Equal(t, getBodyHash([]byte(tc.body)), c.BodyHash)
// 				assert.Equal(t, tc.chunked, c.Stream)
// 				assert.Equal(t, tc.code, c.Code)
// 			}
// 		})
// 	}
// }

type testHandler struct {
	response   []byte
	headers    map[string]string
	code       int
	handleFunc func(w http.ResponseWriter, r *http.Request)
}

func (th *testHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if th.handleFunc != nil {
		th.handleFunc(w, r)
		return
	}
	w.WriteHeader(th.code)
	for k, v := range th.headers {
		w.Header().Add(k, v)
	}
	w.Header().Set("Content-Length", fmt.Sprint(len(th.response)))
	w.Write(th.response)
}

func channelReader(ch chan []byte) (*[]byte, func() bool) {
	received := make([]byte, 0, 16)
	return &received, func() bool {
		select {
		case received = <-ch:
			return true
		default:
			return false
		}
	}
}

// func (c *asyncCounter) reset() {
// 	atomic.StoreUint64(&c.c, 0)
// }

func cleanUp() {
	dataDirName = ""
	client = nil
	dataDirName = ""
	cmdHost = "localhost"
	cmdPort = 8080
	flushInterval = defaultFlushInterval
}

func execute(args string) string {
	actual := new(bytes.Buffer)
	rootCmd.SetOut(actual)
	rootCmd.SetErr(actual)
	rootCmd.SetArgs(strings.Split(args, " "))
	rootCmd.Execute()
	return actual.String()
}

func TestMainFunc(t *testing.T) {
	msg := execute("-h")
	require.Contains(t, msg, "Usage:")
	require.Contains(t, msg, version)
	msg = execute(" ")
	require.Contains(t, msg, "Usage:")
	msg = execute("-v")
	require.Contains(t, msg, "Usage:")
}

func TestRootFunc(t *testing.T) {
	cleanUp()
	defer cleanUp()
	cmd := &cobra.Command{}
	pid := syscall.Getpid()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		t.Log("killing")
		syscall.Kill(pid, syscall.SIGINT)
		time.Sleep(5 * time.Millisecond)

	}()
	root(cmd, []string{})
}

func TestRootFunc2(t *testing.T) {
	cleanUp()
	defer cleanUp()
	cmd := &cobra.Command{}
	defer func() { os.Remove("www.example.com") }()
	pid := syscall.Getpid()
	go func() {
		time.Sleep(1500 * time.Millisecond)
		t.Log("killing")
		syscall.Kill(pid, syscall.SIGINT)
		time.Sleep(5 * time.Millisecond)

	}()
	root(cmd, []string{})
}

func TestReqKey(t *testing.T) {
	rBody := `{"request": "getDetailedPairsStats([{id:0x6435ec7f:42161},{id: 0xe975dc7e:42161}]){some data to return}"}`
	re := regexp.MustCompile(`0x[0-9a-f]+:[0-9]+`)
	bodyFiltered := strings.Join(re.FindAllString(rBody, -1), "")
	t.Logf("%+v", bodyFiltered)
	url := "/some/path"
	key := url[1:] + bodyFiltered
	t.Log(key)
	t.Logf("%X", sha256.Sum256([]byte(key)))
}
