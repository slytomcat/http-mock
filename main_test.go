package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

func cleanUp() func() {
	storedDataDirName := dataDirName
	storedCmdHost := cmdHost
	storedCmdPort := cmdPort
	storedHandlers := handlers
	return func() {
		dataDirName = storedDataDirName
		cmdHost = storedCmdHost
		cmdPort = storedCmdPort
		rootCmd = &cobra.Command{
			Use:     "http-mock -s localhost -p 8080",
			Short:   fmt.Sprintf("http-mock is proxy/mock service v. %s", version),
			Run:     root,
			Example: "http-mock",
			Version: version,
		}
		handlers = storedHandlers
		_ = os.RemoveAll(dataDirName)
	}
}

func execute(args string) string {
	defer cleanUp()()
	actual := new(bytes.Buffer)
	rootCmd.SetOut(actual)
	rootCmd.SetErr(actual)
	rootCmd.SetArgs(strings.Split(args, " "))
	main()
	return actual.String()
}

func TestMainFunc(t *testing.T) {
	msg := execute("-h")
	require.Contains(t, msg, "Usage:")
	require.Contains(t, msg, version)
	msg = execute("-v")
	require.NotContains(t, msg, "Usage:")
	require.Contains(t, msg, version)
}

func killIt(delay time.Duration) {
	pid := syscall.Getpid()
	go func() {
		time.Sleep(delay)
		syscall.Kill(pid, syscall.SIGINT)
	}()
}

func TestMainByEnv(t *testing.T) {
	host := "localhost"
	port := 8099
	data := "_STORAGE"
	t.Setenv("MANAGEMENT_PORT", fmt.Sprint(port))
	t.Setenv("MANAGEMENT_HOST", host)
	t.Setenv("MANAGEMENT_DATA", data)
	defer cleanUp()()
	killIt(20 * time.Millisecond)
	main()
	defer func() {
		_ = os.RemoveAll(data)
	}()
	time.Sleep(time.Millisecond)
	require.Equal(t, host, cmdHost)
	require.Equal(t, port, cmdPort)
	require.Equal(t, data, dataDirName)
}

func TestService(t *testing.T) {
	msg := make(chan string, 1)
	go func() {
		msg <- execute("")
	}()
	defer func() {
		killIt(time.Millisecond)
		t.Log(<-msg)
	}()
	time.Sleep(50 * time.Millisecond)

	resp, body, err := executeRequest(http.MethodGet, "http://localhost:8080/", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "http-mock service v. local_build", string(body))
	resp, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "config parsing error: json parsing error: unexpected end of JSON input", string(body))
	resp, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", bytes.NewReader([]byte(`}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "config parsing error: json parsing error: invalid character '}' looking for beginning of value", string(body))
	resp, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", bytes.NewReader([]byte(`{"id":"test","port":8090, "responses":[{"url": "/url", "code":200, "response":[{"data":"ok"}]},{"url": "/url", "code":200, "response":[{"data":"ok"}]}]}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "config parsing error: record #1 is duplicate with one of previous", string(body))
	resp, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", bytes.NewReader([]byte(`{"id":"test","port":8090, "responses":[{"url": "/url", "code":200, "response":[{"data":"ok"}]}]}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, `{"id":"test","status":"inactive"}`, string(body))
	data := struct{ ID string }{}
	err = json.Unmarshal(body, &data)
	require.NoError(t, err)
	require.Equal(t, "test", data.ID)
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/start?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, `{"ID":"test","status":"active","port":8090,"passthrough-re":"^$","url-re":"^.*$","body-re":"^.*$","responses":[{"id":"766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3","url":"/url","code":200,"response":[{"data":"ok"}]}]}`, string(body))
	cfg := body
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, body, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), bytes.NewReader([]byte(`}{`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "json parsing error: invalid character '}' looking for beginning of value", string(body))
	resp, body, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), bytes.NewReader(cfg))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, string(body))
	require.Equal(t, "", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "update contain ID with wrong length", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=888BAD1BAD1E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "response 888BAD1BAD1E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3 not found", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=", "wrong"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, `{"id":"766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3","url":"/url","code":200,"response":[{"data":"ok"}]}`, string(body))
	response := body
	resp, body, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/response?id=%s", data.ID), bytes.NewReader(response))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, _, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/response?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, _, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/response?id=%s", "wrong"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, body, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, _, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, _, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=", "wrong"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, _, err = executeRequest(http.MethodGet, "http://localhost:8080/dump-configs", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, body, err = executeRequest(http.MethodGet, "http://localhost:8080/load-configs", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "", string(body))
	resp, body, err = executeRequest(http.MethodGet, "http://localhost:8080/list", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, `[{"id":"test","status":"inactive"}]`, string(body))
	data.ID = "wrong"
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/start?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	resp, body, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	resp, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/wrong?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, resp.StatusCode)
	require.Equal(t, "wrong url: GET/wrong", string(body))
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
