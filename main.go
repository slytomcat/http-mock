package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	version     = "local_build"
	dataDirName string
	cmdHost     string
	cmdPort     int
	logger      = log.New(os.Stdout, "", log.LUTC|log.LstdFlags|log.Lmicroseconds|log.Lmsgprefix)
	handlers    = map[string]*Handler{}
	rootCmd     = &cobra.Command{
		Use:     "http-mock -s localhost -p 8080",
		Short:   fmt.Sprintf("http-mock is proxy/mock service v. %s", version),
		Run:     root,
		Example: "http-mock",
		Version: version,
	}
)

func init() {
	rootCmd.Flags().StringVarP(&cmdHost, "host", "s", "localhost", "host to start service")
	rootCmd.Flags().IntVarP(&cmdPort, "port", "p", 8080, "port to start service")
	rootCmd.Flags().StringVarP(&dataDirName, "data", "d", "_storage", "path for configs storage")
}

func main() {
	rootCmd.Execute()
}

func root(cmd *cobra.Command, _ []string) {
	loadConfigs()
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleCommands)
	server := http.Server{
		Addr:    fmt.Sprintf("%s:%d", cmdHost, cmdPort),
		Handler: mux,
	}
	go func() { logger.Println(server.ListenAndServe()) }()
	logger.Printf("Starting the management service on %s\n", server.Addr)
	sig := make(chan (os.Signal), 3)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	s := <-sig // wait for a signal
	logger.Printf("%s received. Starting shutdown...", s.String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	err := server.Shutdown(ctx)
	if err != nil {
		logger.Printf("Shutdown error:%v", err)
	}
	logger.Println("Shutdown finished.")
	dumpConfigs()
}

func handleCommands(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	switch r.Method + r.URL.Path {
	case "POST/new":
		handler, err := NewHandler(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		handlers[handler.id] = handler
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"id": "%s"}`, handler.id)))
	case "GET/start":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			if err := h.Start(); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case "GET/config":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			w.WriteHeader(http.StatusOK)
			w.Write(h.GetConfig())
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case "POST/config":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			err := h.SetConfig(body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case "GET/stop":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			if err := h.Stop(); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	case "GET/dump_configs":
		dumpConfigs()
		w.WriteHeader(http.StatusOK)
	case "GET/load_configs":
		loadConfigs()
		w.WriteHeader(http.StatusOK)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func dumpConfigs() {
	_ = os.MkdirAll(dataDirName, 01775)
	for _, h := range handlers {
		h.Stop()
		cfg := h.GetConfig()
		err := os.WriteFile(fmt.Sprintf("%s/%s.json", dataDirName, h.id), cfg, 0664)
		if err != nil {
			logger.Printf("Storing config for handler %s error: %v\n", h.id, err)
		}
	}
	logger.Printf("Config dumps are stored into %s\n", dataDirName)
}

func loadConfigs() {
	files, err := os.ReadDir(dataDirName)
	if err != nil {
		logger.Printf("reading storage folder %s error: %v\n", dataDirName, err)
		return
	}
	for _, file := range files {
		fileName := path.Join(dataDirName, file.Name())
		cfg, err := os.ReadFile(fileName)
		if err != nil {
			logger.Printf("reading config file %s error: %v\n", fileName, err)
			continue
		}
		id := strings.Split(fileName, ".")[0]
		if handler, ok := handlers[id]; ok {
			if err := handler.SetConfig(cfg); err != nil {
				logger.Printf("setting config from %s for handler %s error: %v\n", fileName, id, err)
			}
			continue
		}
		if handler, err := NewHandler(cfg); err != nil {
			logger.Printf("setting config from %s for new handler error: %v\n", fileName, err)
		} else {
			handlers[handler.id] = handler
		}
	}
	logger.Printf("Config dumps from %s are loaded\n", dataDirName)
}
