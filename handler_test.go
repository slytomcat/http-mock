package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

func TestHandler(t *testing.T) {
	testCases := []struct {
		name        string            // test name
		config      string            // handler config in json format
		creationErr string            // Handler creation error
		startErr    string            // Handler Start error
		method      string            // request method
		url         string            // request url
		body        string            // request body
		headers     map[string]string // request headers
		chunked     bool              // chunked means that Transfer-Encoding: chunked must exists in the response and response have to be chunked
		expCode     int               // expected code
		expHeader   map[string]string // header of received response
		expResp     string            // expected response data
		expChunks   []string          // expecter chunked data
		recAll      bool              // receive all chunked data
		handler     *testHandler      // server handler for forwarding requests
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
			expHeader: map[string]string{"Content-Length": "2"},
			expResp:   "ok",
			handler: &testHandler{
				response: []byte("ok"),
				headers:  map[string]string{},
				code:     200,
			},
		},
		{
			name:      "get compressed response",
			config:    `{"host": "localhost", "port": 8080}`,
			url:       "/compressed",
			method:    http.MethodGet,
			headers:   map[string]string{},
			expCode:   200,
			expHeader: map[string]string{},
			expResp:   "some data that have to be compressed",
			handler: &testHandler{
				response: []byte("some data that have to be compressed"),
				headers:  map[string]string{},
				code:     200,
			},
		},

		// {
		// 	name:      "GET 200 response w body",
		// 	method:    http.MethodGet,
		// 	url:       "/exists",
		// 	headers:   map[string]string{"Accept-Encoding": "gzip/deflate"},
		// 	expCode:   200,
		// 	expResp:   "some data",
		// 	expHeader: map[string]string{"Content-Length": "9"},
		// },
		// {
		// 	name:      "POST 200 response w body re",
		// 	method:    http.MethodPost,
		// 	url:       "/",
		// 	body:      `{"this": "body"}`,
		// 	headers:   map[string]string{"Connection": "close"},
		// 	expCode:   200,
		// 	expResp:   "some data",
		// 	expHeader: map[string]string{"Content-Length": "9"},
		// },
		// {
		// 	name:      "POST 200 response w body hash",
		// 	method:    http.MethodPost,
		// 	url:       "/",
		// 	body:      `{"this": "body"}`,
		// 	expCode:   200,
		// 	expResp:   "some data",
		// 	expHeader: map[string]string{"Content-Length": "9"},
		// 	recAll:    true,
		// },
		// {
		// 	name:      "GET 200 ignore Transfer-Encoding",
		// 	method:    http.MethodGet,
		// 	url:       "/exists",
		// 	expCode:   200,
		// 	expResp:   "some data",
		// 	expHeader: map[string]string{"Content-Length": "9"},
		// 	recAll:    true,
		// },
		// {
		// 	name:    "get 200 chunked response",
		// 	method:  http.MethodGet,
		// 	url:     "/exists",
		// 	chunked: true,
		// 	expCode: 200,
		// 	expChunks: []string{
		// 		"data line #1\n",
		// 		"data line #2\n",
		// 		"data line #3\n",
		// 		"data line #4\n",
		// 	},
		// 	expHeader: map[string]string{"Transfer-Encoding": "chunked"},
		// 	recAll:    true,
		// },
		// {
		// 	name:    "get 200 chunked response not all",
		// 	method:  http.MethodGet,
		// 	url:     "/exists",
		// 	chunked: true,
		// 	expCode: 200,
		// 	expChunks: []string{
		// 		"data line #1\n",
		// 		"data line #2\n",
		// 		"data line #3\n",
		// 	},
		// 	expHeader: map[string]string{"Transfer-Encoding": "chunked"},
		// 	recAll:    false,
		// },
		// {
		// 	name:      "GET 200 w body",
		// 	method:    http.MethodGet,
		// 	url:       "/path",
		// 	headers:   map[string]string{"Connection": "close", "Accept-Encoding": "gzip/deflate"},
		// 	chunked:   false,
		// 	expCode:   200,
		// 	expResp:   "data",
		// 	expHeader: map[string]string{"Content-Length": "4", "Content-Type": "text/plain; charset=utf-8"},
		// },
		// {
		// 	name:    "GET 404 wo body",
		// 	method:  http.MethodGet,
		// 	url:     "/not/exists",
		// 	chunked: false,
		// 	expCode: 404,
		// },
		// {
		// 	name:    "get 200 chunked response",
		// 	method:  http.MethodGet,
		// 	url:     "/exists",
		// 	chunked: true,
		// 	expCode: 200,
		// 	expChunks: []string{
		// 		"data line #1\n",
		// 		"data line #2\n",
		// 		"data line #3\n",
		// 		"data line #4\n",
		// 	},
		// 	expHeader: map[string]string{"Transfer-Encoding": "chunked"},
		// 	recAll:    true,
		// },
		// {
		// 	name:    "get 200 chunked response not all",
		// 	method:  http.MethodGet,
		// 	url:     "/exists",
		// 	chunked: true,
		// 	expCode: 200,
		// 	expChunks: []string{
		// 		"data line #1\n",
		// 		"data line #2\n",
		// 		"data line #3\n",
		// 	},
		// 	expHeader: map[string]string{"Transfer-Encoding": "chunked"},
		// 	recAll:    false,
		// },
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
				client = server.Client()
				defer func() {
					server.Close()
					client = nil
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
				time.Sleep(5 * time.Millisecond) // wait for http server stop
			}()
			time.Sleep(5 * time.Millisecond)
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
				require.Equal(t, "chunked", resp.Header.Get("Transfer-Encoding"))
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
		})
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
	logger.Printf("testHandler request: %+v", r)
	if th.handleFunc != nil {
		th.handleFunc(w, r)
		return
	}
	w.WriteHeader(th.code)
	for k, v := range th.headers {
		w.Header().Add(k, v)
	}
	if len(th.chunks) > 0 {
		w.Header().Set("Transfer-Encoding", "chunked")
		flush := w.(http.Flusher).Flush
		for _, c := range th.chunks {
			time.Sleep(c.delay)
			_, err := w.Write([]byte(c.msg))

			if err != nil {
				return
			}
			flush()
		}
	} else {
		if r.Header.Get("Accept-Encoding") == "gzip" {
			w.Header().Add("Content-Encoding", "gzip")
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(th.response)))
		w.Write(th.response)
	}
}
