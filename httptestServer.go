package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

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
		if len(data) > minCompressSize && strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			w.Header().Set("Content-Encoding", "gzip")
			data = mustCompress(data)
		} else {
			w.Header().Del("Content-Encoding")
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
