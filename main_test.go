package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"
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

func TestMainByEnvFail(t *testing.T) {
	host := "localhost"
	port := "bad_number"
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
	require.Equal(t, defaultCmdPort, cmdPort)
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

	status, body, err := executeRequest(http.MethodGet, "http://localhost:8080/", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "http-mock service v. local_build", string(body))
	status, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "config parsing error: json parsing error: EOF", string(body))
	status, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", bytes.NewReader([]byte(`}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "config parsing error: json parsing error: readObjectStart: expect { or n, but found }, error found in #1 byte of ...|}|..., bigger context ...|}|...", string(body))
	status, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", bytes.NewReader([]byte(`{"id":"test","port":8090, "responses":[{"url": "/url", "code":200, "response":[{"data":"ok"}]},{"url": "/url", "code":200, "response":[{"data":"ok"}]}]}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "config parsing error: record #1 is duplicate with one of previous", string(body))
	status, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", bytes.NewReader([]byte(`{"id":"test","port":8090, "responses":[{"url": "/url", "code":200, "response":[{"data":"ok"}]}]}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, `{"id":"test","status":"inactive"}`, string(body))
	data := struct{ ID string }{}
	err = json.Unmarshal(body, &data)
	require.NoError(t, err)
	require.Equal(t, "test", data.ID)
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/start?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodPost, "http://localhost:8080/new", bytes.NewReader([]byte(`{"id":"test2","port":8090}`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "{\"id\":\"test2\",\"status\":\"inactive\"}", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/start?id=%s", "test2"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, `listen tcp :8090: bind: address already in use`, string(body))
	status, body, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/config?id=%s", "test2"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, `{"ID":"test","status":"active","port":8090,"passthrough-re":"^$","url-re":"^.*$","url-ex-re":"^$","body-re":"^.*$","body-ex-re":"^$","responses":[{"id":"766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3","url":"/url","code":200,"response":[{"data":"ok"}]}]}`, string(body))
	cfg := body
	status, body, err = executeRequest(http.MethodGet, "http://localhost:8080/dump-configs", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	readCfg, err := os.ReadFile(path.Join(dataDirName, "test.json"))
	require.NoError(t, err)
	require.Equal(t, string(cfg), string(readCfg))
	require.NoError(t, os.WriteFile(path.Join(dataDirName, "some.file"), []byte("some data"), 0664))
	status, body, err = executeRequest(http.MethodGet, "http://localhost:8080/load-configs", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, "test handler already stopped", string(body))
	status, body, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), bytes.NewReader([]byte(`}{`)))
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "json parsing error: readObjectStart: expect { or n, but found }, error found in #1 byte of ...|}{|..., bigger context ...|}{|...", string(body))
	status, body, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), bytes.NewReader(cfg))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status, string(body))
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "update contain ID with wrong length", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=888BAD1BAD1E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "response 888BAD1BAD1E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3 not found", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=", "wrong"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, `{"id":"766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3","url":"/url","code":200,"response":[{"data":"ok"}]}`, string(body))
	response := body
	status, body, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/response?id=%s", data.ID), bytes.NewReader(response))
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, _, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/response?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "", string(body))
	status, _, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/response?id=%s", "wrong"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, _, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusBadRequest, status)
	require.Equal(t, "", string(body))
	status, _, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/response?id=%s&resp-id=", "wrong"), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/start?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodGet, "http://localhost:8080/list", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, `[{"id":"test","status":"active"}]`, string(body))
	status, body, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, "", string(body))
	status, body, err = executeRequest(http.MethodDelete, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "no handler found for id 'test'", string(body))
	status, body, err = executeRequest(http.MethodGet, "http://localhost:8080/list", nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, status)
	require.Equal(t, `[]`, string(body))
	data.ID = "wrong"
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/start?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	status, body, err = executeRequest(http.MethodPost, fmt.Sprintf("http://localhost:8080/config?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/stop?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "no handler found for id 'wrong'", string(body))
	status, body, err = executeRequest(http.MethodGet, fmt.Sprintf("http://localhost:8080/wrong?id=%s", data.ID), nil)
	require.NoError(t, err)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "wrong url: GET/wrong", string(body))
}

func executeRequest(method, url string, body io.Reader) (int, []byte, error) {
	req, _ := http.NewRequest(method, url, body)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, nil, err
	}
	return resp.StatusCode, data, nil
}

func checkMgmPortStatus() bool {
	resp, err := http.DefaultClient.Get("http://localhost:8080/")
	return err == nil && resp.StatusCode == 200
}

func copyFolder(srcDir, dstDir string) error {
	if err := os.MkdirAll(dstDir, 01775); err != nil {
		return err
	}
	filesList, err := os.ReadDir(srcDir)
	if err != nil {
		return err
	}
	for _, file := range filesList {
		source := path.Join(srcDir, file.Name())
		data, err := os.ReadFile(source)
		if err != nil {
			return err
		}
		destination := path.Join(dstDir, file.Name())
		err = os.WriteFile(destination, data, 0664)
		if err != nil {
			return err
		}
	}
	return nil
}

func TestProxiesChain(t *testing.T) {
	testStorageFolder := t.TempDir()
	testDataSourceFolder := "proxiesCainTestData"
	require.NoError(t, copyFolder(testDataSourceFolder, testStorageFolder))
	t.Setenv("MANAGEMENT_DATA", testStorageFolder)
	t.Setenv("MANAGEMENT_PORT", "8080")
	t.Setenv("MANAGEMENT_HOST", "localhost")
	go main()
	require.Eventually(t, checkMgmPortStatus, 3*time.Second, 100*time.Millisecond)
	resp, err := http.DefaultClient.Get("http://localhost:8095/symbols?domain=tv&prefix=BINANCE&type=swap,futures,spot&fields=base-currency-id,currency-id,typespecs")
	require.NoError(t, err)
	require.Equal(t, 200, resp.StatusCode)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	sourceResp, err := http.DefaultClient.Get("http://localhost:8090/symbols?domain=tv&prefix=BINANCE&type=swap,futures,spot&fields=base-currency-id,currency-id,typespecs")
	require.NoError(t, err)
	require.Equal(t, 200, sourceResp.StatusCode)
	defer sourceResp.Body.Close()
	source, err := io.ReadAll(sourceResp.Body)
	require.NoError(t, err)
	require.Equal(t, source, data)
	killIt(500 * time.Millisecond)
	time.Sleep(600 * time.Millisecond)
	require.Never(t, checkMgmPortStatus, 1*time.Second, 100*time.Millisecond)
}
