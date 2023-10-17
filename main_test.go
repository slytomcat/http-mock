package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"regexp"
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

type testFile struct {
	name    string
	content string
}

func cleanUp() {
	dataDirName = ""
	client = nil
	dataDirName = ""
	cmdHost = "localhost"
	cmdPort = 8080
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
