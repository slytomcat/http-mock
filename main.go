package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

const (
	mgm            = "management_service"
	defaultCmdPort = 8080
)

var (
	version     = "local_build"
	dataDirName string
	cmdHost     string
	cmdPort     int
	logLevel    string
	logger      = &slog.Logger{}
	handlers    = map[string]*Handler{}
	rootCmd     = &cobra.Command{
		Use:     "http-mock",
		Short:   fmt.Sprintf("http-mock is proxy/mock service v. %s", version),
		Run:     root,
		Example: "http-mock --port 8080 --host localhost\n\nValues for --port, --host, --data can be also set via environment variables MANAGEMENT_PORT, MANAGEMENT_HOST and MANAGEMENT_DATA.",
		Version: version,
	}
)

func init() {
	rootCmd.Flags().StringVarP(&cmdHost, "host", "s", "localhost", "host to start service")
	rootCmd.Flags().IntVarP(&cmdPort, "port", "p", defaultCmdPort, "port to start service")
	rootCmd.Flags().StringVarP(&dataDirName, "data", "d", "_storage", "path for configs storage")
	rootCmd.Flags().StringVarP(&logLevel, "log", "l", "info", "logging level, one of 'error', 'warn', 'info' or 'debug', default: 'info'")
}

func initLogging() {
	logLevelVar := &slog.LevelVar{}
	logLevelVar.UnmarshalText([]byte(strings.ToUpper(logLevel)))
	logger = slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: logLevelVar}))
}

func main() {
	envVarName := "MANAGEMENT_PORT"
	if val, ok := os.LookupEnv(envVarName); ok {
		if port, err := strconv.Atoi(val); err == nil {
			cmdPort = port
		} else {
			fmt.Printf("error parsing value (%s) of environment variable %s: %v", val, envVarName, err)
		}
	}
	if val, ok := os.LookupEnv("MANAGEMENT_HOST"); ok {
		cmdHost = val
	}
	if val, ok := os.LookupEnv("MANAGEMENT_DATA"); ok {
		dataDirName = val
	}
	initLogging()
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
	go func() { logger.Info(mgm, "desc", server.ListenAndServe()) }()
	logger.Info(mgm, "addr", server.Addr, "data_dir", dataDirName)
	sig := make(chan (os.Signal), 3)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	s := <-sig // wait for a signal
	logger.Info(mgm, "signal", s.String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	err := server.Shutdown(ctx)
	if err != nil {
		logger.Error(mgm, "desc", err)
	}
	dumpConfigs()
	for _, h := range handlers {
		h.Stop()
	}
}

func handleCommands(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(err.Error()))
		return
	}
	reqPath := r.Method + r.URL.Path
	switch reqPath {
	case "POST/new":
		handler, err := NewHandler(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error()))
			return
		}
		handlers[handler.id] = handler
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(handler.String()))
	case "GET/list":
		w.WriteHeader(http.StatusOK)
		list := make([]*Status, 0, len(handlers))
		for _, h := range handlers {
			list = append(list, h.GetStatus())
		}
		resp, _ := json.Marshal(list)
		w.Write(resp)
	case "GET/start":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			if err := h.Start(); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(err.Error()))
				return
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("no handler found for id '%s'", id)))
		}
	case "GET/config":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			init, rest := h.GetConfig()
			defer func() {
				for range rest {
				} // drain rest
			}()
			noRest := true
			part := []byte{}
			select {
			case part = <-rest:
			default:
				noRest = false
			}
			if noRest {
				w.WriteHeader(http.StatusOK)
				w.Write(init)
				return
			}
			w.Header().Set("Transfer-Encoding", "chunked")
			w.WriteHeader(http.StatusOK)
			w.Write(init)
			w.Write(part)
			for part = range rest {
				w.Write(part)
			}
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("no handler found for id '%s'", id)))
		}
	case "POST/config":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			err := h.SetConfig(body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(err.Error()))
				return
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("no handler found for id '%s'", id)))
		}
	case "DELETE/config":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			if h.status == "active" {
				if err := h.Stop(); err != nil {
					w.WriteHeader(http.StatusInternalServerError)
					w.Write([]byte(err.Error()))
					return
				}
			}
			delete(handlers, id)
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("no handler found for id '%s'", id)))
		}
	case "GET/stop":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			if err := h.Stop(); err != nil {
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(err.Error()))
				return
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("no handler found for id '%s'", id)))
		}
	case "GET/response":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			respID := r.URL.Query().Get("resp-id")
			data, err := h.GetResponse(respID)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(err.Error()))
			} else {
				w.WriteHeader(http.StatusOK)
				w.Write(data)
			}
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("no handler found for id '%s'", id)))
		}
	case "POST/response":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			err = h.UpdateResponse(body)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(err.Error()))
			} else {
				w.WriteHeader(http.StatusOK)
			}
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("no handler found for id '%s'", id)))
		}
	case "DELETE/response":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			respID := r.URL.Query().Get("resp-id")
			if err = h.DeleteResponse(respID); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				w.Write([]byte(err.Error()))
			} else {
				w.WriteHeader(http.StatusOK)
			}
		} else {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(fmt.Sprintf("no handler found for id '%s'", id)))
		}
	case "GET/dump-configs":
		dumpConfigs()
		w.WriteHeader(http.StatusOK)
	case "GET/load-configs":
		loadConfigs()
		w.WriteHeader(http.StatusOK)
	case "GET/":
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf("http-mock service v. %s", version)))
	default:
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("wrong url: " + reqPath))
	}
}

func dumpConfigs() {
	_ = os.MkdirAll(dataDirName, 01775)
	for _, h := range handlers {
		file, err := os.OpenFile(fmt.Sprintf("%s/%s.json", dataDirName, h.id), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0664)
		if err != nil {
			logger.Error(mgm, "handler", h.id, "desc", err)
			return
		}
		defer file.Close()
		part1, rest := h.GetConfig()
		_, err = file.Write(part1)
		if err != nil {
			logger.Error(mgm, "handler", h.id, "desc", err)
			return
		}
		for part := range rest {
			_, err = file.Write(part)
			if err != nil {
				logger.Error(mgm, "handler", h.id, "desc", err)
				return
			}
		}
		logger.Info(mgm, "handler", h.id, "desc", fmt.Sprintf("config stored to %s/%s.json", dataDirName, h.id))
	}
}

func loadConfigs() {
	files, err := os.ReadDir(dataDirName)
	if err != nil {
		logger.Warn(mgm, "desc", fmt.Sprintf("reading storage folder %s error: %v", dataDirName, err))
		return
	}
	for _, file := range files {
		fileName := file.Name()
		if !strings.HasSuffix(fileName, ".json") {
			continue
		}
		filePath := path.Join(dataDirName, file.Name())
		cfg, err := os.ReadFile(filePath)
		if err != nil {
			logger.Error(mgm, "desc", fmt.Sprintf("reading config file %s error: %v", filePath, err))
			continue
		}
		id := strings.Split(fileName, ".")[0]
		handler, ok := handlers[id]
		if ok {
			if err := handler.SetConfig(cfg); err != nil {
				logger.Error(mgm, "handler", id, "desc", fmt.Sprintf("setting config from %s error: %v", filePath, err))
			}
			logger.Info(mgm, "handler", id, "desc", fmt.Sprintf("config set from %s", filePath))
			continue
		}
		if handler, err := NewHandler(cfg); err != nil {
			logger.Error(mgm, "desc", fmt.Sprintf("setting config from %s for new handler error: %v", filePath, err))
		} else {
			handlers[handler.id] = handler
			id = handler.id
		}
	}
}
