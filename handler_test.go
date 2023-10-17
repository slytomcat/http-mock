package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatChunk(t *testing.T) {
	data := []byte("some_data")
	delay := 123 * time.Millisecond
	require.Equal(t, []byte("                      123|some_data"), formatChink(delay, data))
}

func TestSendHeaders(t *testing.T) {
	header := http.Header{}
	values := map[string]string{"KEY1": "val1", "key2": "val2"}
	sendHeaders(header, values)
	for k, v := range values {
		require.Equal(t, v, header.Get(k))
	}
}

func mustCompress(data []byte) []byte {
	compressed, err := compress(data)
	if err != nil {
		panic(err)
	}
	return compressed
}

func TestParseKeyErrors(t *testing.T) {
	h := Handler{}
	_, err := h.parseKey("AAAA")
	require.EqualError(t, err, "update contain ID with wrong length")
	_, err = h.parseKey("%6CB8D0EA45696134985DD132F7C20D6D6356CD364D44AF7E8313FFF79702361")
	require.EqualError(t, err, "parsing update.ID error: encoding/hex: invalid byte: U+0025 '%'")
}

func TestHandlerSetConfig(t *testing.T) {
	h := &Handler{host: "localhost", port: 8080}
	config := `{"host": "localhost", "port": 8080}`
	err := h.SetConfig([]byte(config))
	require.NoError(t, err)
	require.Equal(t, "", h.forwardURL)
	require.Equal(t, "^$", h.urlRe.String())
	require.Equal(t, "^$", h.passthroughRe.String())
	require.Equal(t, "^$", h.bodyRe.String())
	require.Empty(t, h.responses)
}

func TestHandlerDeleteResponse(t *testing.T) {
	h := &Handler{host: "localhost", port: 8080}
	config := `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":false,"response":"response data"}]}`
	require.NoError(t, h.parseConfig([]byte(config)))
	require.NoError(t, h.DeleteResponse("86CB8D0EA45696134985DD132F7C20D6D6356CD364D44AF7E8313FFF79702361"))
	require.Empty(t, h.responses)
}

func TestHandlerUpdateResponse(t *testing.T) {
	h := &Handler{host: "localhost", port: 8080}
	config := `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":false,"response":"response data"}]}`
	require.NoError(t, h.parseConfig([]byte(config)))
	require.NoError(t, h.UpdateResponse([]byte(`{"id":"86CB8D0EA45696134985DD132F7C20D6D6356CD364D44AF7E8313FFF79702361","url":"/it/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":false,"response":"response data"}`)))
	require.Len(t, h.responses, 1)
	newKey := h.makeKey("/it/exists", "")
	require.Equal(t, "/it/exists", h.responses[newKey].URL)
	require.NotEqual(t, "86CB8D0EA45696134985DD132F7C20D6D6356CD364D44AF7E8313FFF79702361", h.responses[newKey].ID)
}
func TestHandlerUpdateResponseError(t *testing.T) {
	h := &Handler{host: "localhost", port: 8080}
	config := `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":false,"response":"response data"}]}`
	require.NoError(t, h.parseConfig([]byte(config)))
	err := h.UpdateResponse([]byte(`{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":false,"response":"response data"}`))
	require.EqualError(t, err, "update has key that overwrites other response")
}

var (
	longData = strings.Repeat("some very long data to compress ", 32) // 1K data
)

func TestHandler(t *testing.T) {

	testCases := []struct {
		name                  string            // test name
		config                string            // handler config in json format
		creationErr           string            // Handler creation error
		startErr              string            // Handler Start error
		method                string            // request method
		url                   string            // request url
		body                  string            // request body
		headers               map[string]string // request headers
		chunked               bool              // chunked means that Transfer-Encoding: chunked must exists in the response and response have to be chunked
		expCode               int               // expected code
		expHeader             map[string]string // header of received response
		expResp               string            // expected response data
		expChunks             []string          // expecter chunked data
		expConfigParts        []string          // expected config parts that have to be exist in config
		unexpectedConfigParts []string          // unexpected config parts, they shouldn't exists in config
		recAll                bool              // receive all chunked data
		handler               *testHandler      // server handler for forwarding requests
	}{
		{
			name:        "create error",
			config:      `][]`, // wrong json
			creationErr: "config parsing error: json parsing error: invalid character ']' looking for beginning of value",
		}, {
			name:     "start error",
			config:   `{"port": -10}`, // wrong port
			startErr: "listen tcp: address -10: invalid port",
		}, {
			name:      "GET 404 response",
			config:    `{"host": "localhost", "port": 8080}`,
			method:    http.MethodGet,
			url:       "/wrong",
			expCode:   404,
			expHeader: map[string]string{"Content-Length": "0"},
		}, {
			name:      "GET 200 response",
			config:    `{"host": "localhost", "port": 8080}`,
			method:    http.MethodGet,
			url:       "/exists",
			expCode:   200,
			expResp:   "ok",
			expHeader: map[string]string{"Content-Length": "2"},
			handler: &testHandler{
				response: []byte("ok"),
				headers:  map[string]string{},
				code:     200,
			},
			unexpectedConfigParts: []string{`"Content-Encoding":"gzip"`}, // too short response to compress it
		}, {
			name:    "get compressed response",
			config:  `{"host": "localhost", "port": 8080}`,
			url:     "/compressed",
			method:  http.MethodGet,
			expCode: 200,
			expResp: longData,
			handler: &testHandler{
				response: []byte(longData),
				headers:  map[string]string{"Content-Encoding": "gzip"},
				code:     200,
			},
			expConfigParts: []string{`"Content-Encoding":"gzip"`, `some very long data to compress some very long`},
		}, {
			name:      "POST 200 response w body re",
			config:    `{"host": "localhost", "port": 8080}`,
			method:    http.MethodPost,
			url:       "/",
			body:      `{"this": "body"}`,
			headers:   map[string]string{"Connection": "close"},
			expCode:   200,
			expResp:   "some data",
			expHeader: map[string]string{"Content-Length": "9"},
			handler: &testHandler{
				response: []byte("some data"),
				code:     200,
			},
			expConfigParts: []string{`this`, `body`, `some data`},
		}, {
			name:    "passthrough",
			config:  `{"host": "localhost", "port": 8080, "passthrough-re": "^/pass.*$"}`,
			method:  http.MethodGet,
			url:     "/pass/it/through",
			expCode: 200,
			expResp: "some data",
			handler: &testHandler{
				response: []byte("some data"),
				code:     200,
			},
			unexpectedConfigParts: []string{"/pass/it/through", "responses"},
		}, {
			name:    "get 200 chunked response",
			config:  `{"host": "localhost", "port": 8080}`,
			method:  http.MethodGet,
			url:     "/exists",
			chunked: true,
			expCode: 200,
			expChunks: []string{
				"data line #1",
				"data line #2",
				"data line #3",
				"data line #4",
			},
			handler: &testHandler{
				chunks: []chunk{
					{
						delay: 30,
						msg:   "data line #1",
					}, {
						delay: 10,
						msg:   "data line #2",
					}, {
						delay: 10,
						msg:   "data line #3",
					}, {
						delay: 10,
						msg:   "data line #4",
					},
				},
				code: 200,
			},
			expConfigParts: []string{
				"|data line #1",
				"|data line #2",
				"|data line #3",
				"|data line #4",
			},
		}, {
			name:    "get part of chunked response",
			config:  `{"host": "localhost", "port": 8080}`,
			method:  http.MethodGet,
			url:     "/exists",
			chunked: true,
			expCode: 200,
			expChunks: []string{
				"data line #1",
				"data line #2", // receive only 2 of 4 chunks
			},
			handler: &testHandler{
				chunks: []chunk{
					{
						delay: 30,
						msg:   "data line #1",
					}, {
						delay: 10,
						msg:   "data line #2",
					}, {
						delay: 10,
						msg:   "data line #3",
					}, {
						delay: 10,
						msg:   "data line #4",
					},
				},
				code: 200,
			},
			expConfigParts: []string{
				"|data line #1",
				"|data line #2",
				"|data line #3",
				"|data line #4",
			},
		}, {
			name:    "passthrough get 200 chunked response",
			config:  `{"host": "localhost", "port": 8080, "passthrough-re": "^/pass.*$"}`,
			method:  http.MethodGet,
			url:     "/pass/it/through",
			chunked: true,
			expCode: 200,
			expChunks: []string{
				"data line #1",
				"data line #2",
				"data line #3",
				"data line #4",
			},
			handler: &testHandler{
				chunks: []chunk{
					{
						delay: 30,
						msg:   "data line #1",
					}, {
						delay: 10,
						msg:   "data line #2",
					}, {
						delay: 10,
						msg:   "data line #3",
					}, {
						delay: 10,
						msg:   "data line #4",
					},
				},
				code: 200,
			},
			unexpectedConfigParts: []string{
				"/pass/it/through",
				"responses",
				"|data line #1",
				"|data line #2",
				"|data line #3",
				"|data line #4",
			},
		}, {
			name:    "get chunked response from config",
			config:  `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"id":"E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855","url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":true,"response":"                        0|data line #1\n                        0|data line #2\n                        0|data line #3\n                        0|data line #4\n"}]}`,
			method:  http.MethodGet,
			url:     "/exists",
			chunked: true,
			expCode: 200,
			expChunks: []string{
				"data line #1",
				"data line #2",
				"data line #3",
				"data line #4",
			},
		}, {
			name:    "get response from config",
			config:  `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"id":"E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855","url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":false,"response":"response data"}]}`,
			method:  http.MethodGet,
			url:     "/exists",
			expCode: 200,
			expResp: "response data",
		}, {
			name:    "get compressed response from config",
			config:  `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"id":"E3B0C44298FC1C149AFBF4C8996FB92427AE41E4649B934CA495991B7852B855","url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT","Content-Encoding":"gzip"},"chunked":false,"response":"response data"}]}`,
			method:  http.MethodGet,
			url:     "/exists",
			expCode: 200,
			expResp: "response data",
		},
	}
	for _, tc := range testCases {
		testName := tc.name
		t.Run(testName, func(t *testing.T) {
			h, err := NewHandler([]byte(tc.config))
			if tc.creationErr != "" {
				require.EqualError(t, err, tc.creationErr)
				return
			}
			require.NoError(t, err)
			if tc.handler != nil {
				server := httptest.NewServer(tc.handler)
				defer func() {
					server.Close()
				}()
				h.forwardURL = server.URL
			}
			url := fmt.Sprintf("http://%s:%d%s", h.host, h.port, tc.url)
			err = h.Start()
			if tc.startErr != "" {
				require.EqualError(t, err, tc.startErr)
				return
			}
			require.NoError(t, err)
			defer func() {
				require.NoError(t, h.Stop())
				time.Sleep(30 * time.Millisecond) // wait for http server stop
			}()
			time.Sleep(30 * time.Millisecond) // wait for http server start
			req, _ := http.NewRequest(tc.method, url, bytes.NewReader([]byte(tc.body)))
			for k, v := range tc.headers {
				req.Header.Add(k, v)
			}
			resp, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			require.Equal(t, tc.expCode, resp.StatusCode)
			for k, v := range tc.expHeader {
				assert.Equal(t, v, resp.Header.Get(k))
			}
			if tc.chunked {
				require.Len(t, resp.TransferEncoding, 1)
				require.Equal(t, "chunked", resp.TransferEncoding[0])
				scanner := bufio.NewScanner(resp.Body)
				for _, ch := range tc.expChunks {
					if scanner.Scan() {
						assert.Equal(t, ch, scanner.Text())
					} else {
						t.Fatal("not all expected chunks received")
					}
				}
			} else {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, tc.expResp, string(body))
			}
			cnf := string(h.GetConfig())
			// t.Logf("config: %s", cnf)
			require.NotEmpty(t, cnf)
			if len(tc.expConfigParts) > 0 {
				for _, part := range tc.expConfigParts {
					assert.Contains(t, cnf, part)
				}
			}
			if len(tc.unexpectedConfigParts) > 0 {
				for _, part := range tc.expConfigParts {
					assert.NotContains(t, cnf, part)
				}
			}
		})
	}
}

func TestHandlerSequence(t *testing.T) {
	h, err := NewHandler([]byte(`{"host": "localhost", "port": 8080, "url-re": "^/.*$"}`))
	require.NoError(t, err)
	var extCalls int64
	response := "response data"
	server := httptest.NewServer(&testHandler{
		handleFunc: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
			atomic.AddInt64(&extCalls, 1)
		}})
	defer func() {
		server.Close()
	}()
	h.forwardURL = server.URL
	err = h.Start()
	require.NoError(t, err)
	time.Sleep(30 * time.Microsecond) // wait for servers start
	url := "http://localhost:8080/some"
	req, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := client.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, int64(1), atomic.LoadInt64(&extCalls))
	respData, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, response, string(respData))
	req2, err := http.NewRequest(http.MethodGet, url, nil)
	require.NoError(t, err)
	resp2, err := client.Do(req2)
	require.NoError(t, err)
	defer resp2.Body.Close()
	require.Equal(t, int64(1), atomic.LoadInt64(&extCalls))
	respData2, err := io.ReadAll(resp2.Body)
	require.NoError(t, err)
	require.Equal(t, response, string(respData2))
}

type chunk struct {
	delay time.Duration
	msg   string
}

type testHandler struct {
	response   []byte
	chunks     []chunk
	headers    map[string]string
	code       int
	handleFunc func(w http.ResponseWriter, r *http.Request)
}

func (th *testHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// logger.Printf("testHandler request: %+v", r)
	if th.handleFunc != nil {
		th.handleFunc(w, r)
		return
	}
	for k, v := range th.headers {
		w.Header().Set(k, v)
	}
	if len(th.chunks) > 0 {
		w.Header().Set("Transfer-Encoding", "chunked")
		flush := w.(http.Flusher).Flush
		w.WriteHeader(th.code)
		for _, c := range th.chunks {
			time.Sleep(c.delay)
			_, err := w.Write([]byte(c.msg + "\n"))

			if err != nil {
				return
			}
			flush()
		}
	} else {
		data := th.response
		if len(data) > 255 && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			data = mustCompress(data)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(data)))
		w.WriteHeader(th.code)
		w.Write(data)
	}

}

func TestTestHandler(t *testing.T) {
	server := httptest.NewServer(&testHandler{
		response: []byte(longData),
		headers:  map[string]string{"Content-Encoding": "gzip"},
		code:     200,
	})
	TestClient := server.Client()
	defer func() {
		server.Close()
		TestClient = nil
	}()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/some", nil)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond) // wait for test server start
	resp, err := TestClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	t.Logf("resp: %+v", resp)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, longData, string(body))

}

func TestTestHandlerChunked(t *testing.T) {
	tHandler := &testHandler{
		chunks: []chunk{
			{
				delay: 30,
				msg:   "data line #1",
			}, {
				delay: 10,
				msg:   "data line #2",
			}, {
				delay: 10,
				msg:   "data line #3",
			}, {
				delay: 10,
				msg:   "data line #4",
			},
		},
		code: 200,
	}
	server := httptest.NewServer(tHandler)
	TestClient := server.Client()
	defer func() {
		server.Close()
		TestClient = nil
	}()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/some", nil)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond) // wait for test server start
	resp, err := TestClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	t.Logf("resp: %+v", resp)
	require.Len(t, resp.TransferEncoding, 1)
	require.Equal(t, "chunked", resp.TransferEncoding[0])
	scanner := bufio.NewScanner(resp.Body)
	i := 0
	for scanner.Scan() {
		line := scanner.Text()
		assert.Equal(t, tHandler.chunks[i].msg, line)
		i++
	}
}
