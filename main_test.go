package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
)

const (
	notExistingPath = "/notExists/path"
)

func cleanUp() {
	dataDirName = ""
	cmdHost = "localhost"
	cmdPort = 8080
	rootCmd = &cobra.Command{
		Use:     "http-mock -s localhost -p 8080",
		Short:   fmt.Sprintf("http-mock is proxy/mock service v. %s", version),
		Run:     root,
		Example: "http-mock",
		Version: version,
	}
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
}

func killIt(delay time.Duration) {
	pid := syscall.Getpid()
	go func() {
		time.Sleep(delay)
		syscall.Kill(pid, syscall.SIGINT)
	}()
}

func TestRootFunc(t *testing.T) {
	cleanUp()
	defer cleanUp()
	cmd := &cobra.Command{}
	killIt(150 * time.Millisecond)
	root(cmd, []string{})
}

func TestService(t *testing.T) {
	cleanUp()
	defer cleanUp()
	msg := make(chan string, 1)
	go func() {
		msg <- execute("")
	}()
	defer func() {
		killIt(100 * time.Millisecond)
		t.Log(<-msg)
	}()
	time.Sleep(50 * time.Millisecond)

	resp, body, err := executeRequest(http.MethodGet, "http://localhost:8080/new", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Empty(t, body)
	resp, body, err = executeRequest(http.MethodGet, "http://localhost:8080/new", bytes.NewReader([]byte(`}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, body, err = executeRequest(http.MethodGet, "http://localhost:8080/new", bytes.NewReader([]byte(`{"host": "localhost","port":8090}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Logf("New: %s", body)
	data := struct{ ID string }{}
	err = json.Unmarshal(body, &data)
	resp, body, err = executeRequest(http.MethodPatch, fmt.Sprintf("http://localhost:8080/start?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	t.Logf("Config: %s", body)
	resp, _, err = executeRequest(http.MethodPut, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), bytes.NewReader([]byte(`}{`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	resp, body, err = executeRequest(http.MethodPut, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), bytes.NewReader(body))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	resp, body, err = executeRequest(http.MethodPatch, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	data.ID = "wrong"
	resp, body, err = executeRequest(http.MethodPatch, fmt.Sprintf("http://localhost:8080/start?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, body, err = executeRequest(http.MethodPut, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, body, err = executeRequest(http.MethodPatch, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	resp, body, err = executeRequest(http.MethodPatch, fmt.Sprintf("http://localhost:8080/wrong?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	time.Sleep(50 * time.Millisecond)

	killIt(100 * time.Millisecond)
}

func executeRequest(method, url string, body io.Reader) (*http.Response, []byte, error) {
	req, _ := http.NewRequest(method, url, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	return resp, data, nil
}
