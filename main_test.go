package main

import (
	"bytes"
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

func killIt(delay time.Duration) {
	pid := syscall.Getpid()
	go func() {
		time.Sleep(delay)
		syscall.Kill(pid, syscall.SIGINT)
		time.Sleep(50 * time.Millisecond)
	}()
}

func TestRootFunc(t *testing.T) {
	cleanUp()
	defer cleanUp()
	cmd := &cobra.Command{}
	killIt(1500 * time.Millisecond)
	root(cmd, []string{})
}
