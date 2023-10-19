package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatChunk(t *testing.T) {
	data := []byte("some_data")
	delay := 123 * time.Millisecond
	require.Equal(t, []byte("         123|some_data"), formatChink(delay, data))
}

func TestSendHeaders(t *testing.T) {
	header := http.Header{}
	values := map[string]string{"KEY1": "val1", "key2": "val2"}
	sendHeaders(header, values)
	for k, v := range values {
		require.Equal(t, v, header.Get(k))
	}
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
	require.Equal(t, "^.*$", h.urlRe.String())
	require.Equal(t, "^$", h.passthroughRe.String())
	require.Equal(t, "^.*$", h.bodyRe.String())
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
	require.Error(t, h.UpdateResponse([]byte(`}`)))
}
func TestHandlerUpdateResponseError(t *testing.T) {
	h := &Handler{host: "localhost", port: 8080}
	config := `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":false,"response":"response data"}]}`
	require.NoError(t, h.parseConfig([]byte(config)))
	err := h.UpdateResponse([]byte(`{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":false,"response":"response data"}`))
	require.EqualError(t, err, "update has key that overwrites other response")
}

var (
	longData   = strings.Repeat("some very long data to compress ", 32) // 1K data
	testChunks = []chunk{
		{delay: 30, msg: "data line #1"},
		{delay: 30, msg: "data line #2"},
		{delay: 30, msg: "data line #3"},
		{delay: 30, msg: "data line #4"},
		{delay: 30, msg: "data line #5"},
		{delay: 30, msg: "data line #6"},
		{delay: 30, msg: "data line #7"},
		{delay: 30, msg: "data line #8"},
		{delay: 30, msg: "data line #9"},
		{delay: 30, msg: "data line #10"},
		{delay: 30, msg: "data line #11"},
		{delay: 30, msg: "data line #12"},
		{delay: 30, msg: "data line #13"},
		{delay: 30, msg: "data line #14"},
		{delay: 30, msg: "data line #15"},
		{delay: 30, msg: "data line #16"},
		{delay: 30, msg: "data line #17"},
		{delay: 30, msg: "data line #18"},
		{delay: 30, msg: "data line #19"},
		{delay: 30, msg: "data line #20"},
		{delay: 30, msg: "data line #21"},
	}
	expChunks = []string{
		"data line #1",
		"data line #2",
		"data line #3",
		"data line #4",
	}
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
		handler               *testHandler      // server handler for forwarding requests
		cancelConn            bool              // call context cancel immediately after receiving the data
	}{
		{
			name:        "create error",
			config:      `][]`, // wrong json
			creationErr: "config parsing error: json parsing error: invalid character ']' looking for beginning of value",
		},
		{
			name:     "start error",
			config:   `{"port": -10}`, // wrong port
			startErr: "listen tcp: address -10: invalid port",
		},
		{
			name:      "GET 404 response",
			config:    `{"host": "localhost", "port": 8080}`,
			method:    http.MethodGet,
			url:       "/wrong",
			expCode:   404,
			expHeader: map[string]string{"Content-Length": "0"},
		},
		{
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
		},
		{
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
		},
		{
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
		},
		{
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
		},
		{
			name:      "get 200 chunked response",
			config:    `{"host": "localhost", "port": 8080}`,
			method:    http.MethodGet,
			url:       "/exists",
			chunked:   true,
			expCode:   200,
			expChunks: expChunks,
			handler: &testHandler{
				code:   200,
				chunks: testChunks[:4],
			},
			expConfigParts: expChunks,
		},
		{
			name:       "get part of chunked response",
			config:     `{"host": "localhost", "port": 8080}`,
			method:     http.MethodGet,
			url:        "/exists",
			chunked:    true,
			expCode:    200,
			expChunks:  expChunks[:2], // receive only 2 chunks
			cancelConn: true,          // cancel connection ctx after receiving data
			handler: &testHandler{
				code:   200,
				chunks: testChunks,
			},
			expConfigParts: expChunks[:2],
		},
		{
			name:      "passthrough get 200 chunked response",
			config:    `{"host": "localhost", "port": 8080, "passthrough-re": "^/pass.*$"}`,
			method:    http.MethodGet,
			url:       "/pass/it/through",
			chunked:   true,
			expCode:   200,
			expChunks: expChunks,
			handler: &testHandler{
				code:   200,
				chunks: testChunks[:4],
			},
			unexpectedConfigParts: append([]string{
				"/pass/it/through",
				"responses"}, expChunks...),
		},
		{
			name:      "get chunked response from config",
			config:    `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"chunked":true,"response":"           0|data line #1\n           0|data line #2\n           0|data line #3\n           0|data line #4\n"}]}`,
			method:    http.MethodGet,
			url:       "/exists",
			chunked:   true,
			expCode:   200,
			expChunks: expChunks,
		},
		{
			name:    "get response from config",
			config:  `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT"},"response":"response data"}]}`,
			method:  http.MethodGet,
			url:     "/exists",
			expCode: 200,
			expResp: "response data",
		},
		{
			name:    "get compressed response from config",
			config:  `{"host":"localhost","port":8080,"url-re":"^.*$","responses":[{"url":"/exists","code":200,"headers":{"Date":"Tue, 17 Oct 2023 13:13:20 GMT","Content-Encoding":"gzip"},"response":"` + longData + `"}]}`,
			method:  http.MethodGet,
			url:     "/exists",
			expCode: 200,
			expResp: longData,
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
			var server *httptest.Server
			if tc.handler != nil {
				server = httptest.NewServer(tc.handler)
				defer func() {
					server.Close()
					time.Sleep(30 * time.Millisecond) // wait for http servers stop
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
			defer h.Stop()
			time.Sleep(30 * time.Millisecond) // wait for http servers start
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			req, _ := http.NewRequestWithContext(ctx, tc.method, url, bytes.NewReader([]byte(tc.body)))
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
				reader := bufio.NewReader(resp.Body)
				for _, ch := range tc.expChunks {
					if chunk, _, err := reader.ReadLine(); err != nil {
						if !errors.Is(err, io.EOF) {
							t.Logf("reading response error: %v", err)
						}
						break
					} else {
						assert.Equal(t, ch, string(chunk))
						// logger.Printf("test        :<- '%s'", chunk)
					}
				}
			} else {
				body, err := io.ReadAll(resp.Body)
				require.NoError(t, err)
				require.Equal(t, tc.expResp, string(body))
			}
			if tc.cancelConn {
				resp.Body.Close()
				cancel()
				logger.Printf("Connection context canceled")
				time.Sleep(50 * time.Millisecond)
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

func TestHandlerDoubleStartStop(t *testing.T) {
	h, err := NewHandler([]byte(`{"host": "localhost", "port": 8080, "url-re": "^/.*$"}`))
	require.NoError(t, err)
	err = h.Start()
	require.NoError(t, err)
	err = h.Start()
	require.Error(t, err)
	err = h.Stop()
	require.NoError(t, err)
	err = h.Stop()
	require.Error(t, err)
}

func TestHandlerSequence(t *testing.T) {
	h, err := NewHandler([]byte(`{"host": "localhost", "port": 8080, "url-re": "^/.*$"}`))
	require.NoError(t, err)
	var extCalls int64
	response := "response data"
	extServer := httptest.NewServer(&testHandler{
		handleFunc: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(response))
			atomic.AddInt64(&extCalls, 1)
		}})
	defer extServer.Close()
	h.forwardURL = extServer.URL
	err = h.Start()
	require.NoError(t, err)
	defer h.Stop()
	time.Sleep(30 * time.Microsecond) // wait for servers start
	req, err := http.NewRequest(http.MethodGet, "http://localhost:8080/some", nil)
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		resp, err := client.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		require.Equal(t, int64(1), atomic.LoadInt64(&extCalls)) // only first request goes to external service
		respData, err := io.ReadAll(resp.Body)
		require.NoError(t, err)
		require.Equal(t, response, string(respData))
	}
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
	if th.handleFunc != nil {
		th.handleFunc(w, r)
		return
	}
	logger.Printf("testHandler: %s %s", r.Method, r.URL.String())
	defer func() {
		logger.Printf("testHandler exits: %s %s", r.Method, r.URL.String())
	}()
	for k, v := range th.headers {
		w.Header().Set(k, v)
	}
	if len(th.chunks) > 0 {
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Header().Del("Content-Encoding") // compression in chunked mode is not supported
		flush := w.(http.Flusher).Flush
		w.WriteHeader(th.code)
		flush()
		// tick := time.Now()
		for _, c := range th.chunks {
			time.Sleep(c.delay * time.Millisecond)
			// logger.Printf("testHandler :%v -> '%s'\n", time.Since(tick), c.msg)
			// tick = time.Now()
			_, err := w.Write([]byte(c.msg + "\n"))
			if err != nil {
				logger.Printf("testHandler for %s %s writing error: %v ", r.Method, r.URL.String(), err)
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

func mustCompress(data []byte) []byte {
	compressed, err := compress(data)
	if err != nil {
		panic(err)
	}
	return compressed
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
		time.Sleep(50 * time.Millisecond)
	}()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/some", nil)
	require.NoError(t, err)
	time.Sleep(30 * time.Millisecond) // wait for test server start
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
		code:   200,
		chunks: testChunks[:4],
	}
	server := httptest.NewServer(tHandler)
	TestClient := server.Client()
	defer func() {
		server.Close()
		TestClient = nil
		time.Sleep(50 * time.Millisecond)
	}()
	req, err := http.NewRequest(http.MethodGet, server.URL+"/some", nil)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond) // wait for test server start
	resp, err := TestClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	// t.Logf("resp: %+v", resp)
	require.Len(t, resp.TransferEncoding, 1)
	require.Equal(t, "chunked", resp.TransferEncoding[0])
	reader := bufio.NewReader(resp.Body)
	i := 0
	tick := time.Now()
	for {
		line, _, err := reader.ReadLine()
		if err != nil {
			t.Logf("%v", err)
			break
		}
		delay := time.Since(tick)
		tick = time.Now()
		// fmt.Printf("    %v -> '%s'\n", delay, line)
		assert.InDelta(t, tHandler.chunks[i].delay*time.Millisecond, delay, 1e6, i)
		assert.Equal(t, tHandler.chunks[i].msg, string(line), 1)
		i++
	}
}

func TestTestHandlerChunkedInteruption(t *testing.T) {
	tHandler := &testHandler{
		code:   200,
		chunks: testChunks,
	}
	server := httptest.NewServer(tHandler)
	TestClient := server.Client()
	defer func() {
		server.Close()
		TestClient = nil
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/some", nil)
	require.NoError(t, err)
	time.Sleep(5 * time.Millisecond) // wait for test server start
	resp, err := TestClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	// t.Logf("resp: %+v", resp)
	require.Len(t, resp.TransferEncoding, 1)
	require.Equal(t, "chunked", resp.TransferEncoding[0])
	reader := bufio.NewReader(resp.Body)
	i := 0
	tick := time.Now()
	for {
		line, _, err := reader.ReadLine()
		if err != nil {
			t.Logf("%v", err)
			break
		}
		delay := time.Since(tick)
		tick = time.Now()
		// fmt.Printf("    %v <- '%s'\n", delay, line)
		assert.InDelta(t, tHandler.chunks[i].delay*time.Millisecond, delay, 3e6, i)
		assert.Equal(t, tHandler.chunks[i].msg, string(line), 1)
		i++
		if i >= 7 {
			resp.Body.Close()
			cancel()
			break
		}
	}
	time.Sleep(50 * time.Millisecond) // to get all logs
}
func TestLive(t *testing.T) {
	t.Skip("live test") // require activated VPN
	configFileName := "config.json"
	cfg, err := os.ReadFile(configFileName)
	if err != nil {
		cfg = []byte(`{"host": "localhost", "port": 8080, "forward-url": "http://udf-nyc.xstaging.tv/hub0"}`)
	}
	h, err := NewHandler(cfg)
	require.NoError(t, err)
	require.NoError(t, h.Start())
	defer func() {
		cfg := h.GetConfig()
		t.Logf("config: %s", string(cfg))
		t.Logf("config length: %d", len(cfg))
		_ = h.Stop()
		_ = os.WriteFile(configFileName, cfg, 0677)
	}()
	sig := make(chan (os.Signal), 3)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	select {
	case <-sig: // wait for a signal
		return
	case <-time.After(170 * time.Second):
		return
	}
}
