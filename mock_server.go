package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type testHandler struct {
	response   []byte
	chunks     []Chunk
	headers    map[string]string
	code       int
	handleFunc func(w http.ResponseWriter, r *http.Request)
}

func (th *testHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if th.handleFunc != nil {
		th.handleFunc(w, r)
		return
	}
	req := fmt.Sprintf("%s %s", r.Method, r.URL.String())
	logger.Info("testHandler", "req", req)
	defer func() {
		logger.Info("testHandler", "handled", req)
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
			time.Sleep(time.Duration(c.Delay) * time.Millisecond)
			// logger.Printf("testHandler :%v -> '%s'\n", time.Since(tick), c.msg)
			// tick = time.Now()
			_, err := w.Write([]byte(c.Data + "\n"))
			if err != nil {
				logger.Error("testHandler", "req", req, "error", err)
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
