package main

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
	require.Equal(t, true, resp.Uncompressed)
}

func TestTestHandlerShort(t *testing.T) {
	shortData := "short data example"
	server := httptest.NewServer(&testHandler{
		response: []byte(shortData),
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
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	t.Logf("resp: %+v, body len: %v", resp, len(body))
	require.Equal(t, shortData, string(body))
	require.Equal(t, false, resp.Uncompressed)
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
	time.Sleep(5 * time.Millisecond) // wait for test server start
	req, err := http.NewRequest(http.MethodGet, server.URL+"/some", nil)
	require.NoError(t, err)
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
			t.Logf("error: %v", err)
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

func TestTestHandlerChunkedInterruption(t *testing.T) {
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
		assert.InDelta(t, tHandler.chunks[i].delay*time.Millisecond, delay, 4e6, i)
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
