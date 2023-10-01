package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

type filter struct {
	re      *regexp.Regexp
	path    string
	code    int
	headers map[string]string
	stream  bool
}

type cfgValue struct {
	Re      string            `json:"re"`
	Path    string            `json:"path"`
	Code    int               `json:"code,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Stream  bool              `json:"stream,omitempty"`
}

const (
	delaySize = 25
)

var (
	version      = "local build"
	forwardURL   string
	forwardHost  string
	config       string
	host         string
	port         int
	printVersion bool
	reURL        = regexp.MustCompile(`https?://([^/]+).*`)
	filters      []filter
	dataPath     string
	cnt          asyncCounter
	cfg          []cfgValue
	cfgLock      sync.Mutex
)

func main() {
	rootCmd := &cobra.Command{
		Use:     "http-mock -c <config file path> | -f <URL for request forwarding>",
		Short:   fmt.Sprintf("http-mock is proxy/mock service v. %s", version),
		Run:     root,
		Example: "http-mock -c config.json -p 8000\nor\nhttp-mock -f http://examle.com",
	}
	rootCmd.Flags().StringVarP(&host, "host", "s", "localhost", "host to start service")
	rootCmd.Flags().IntVarP(&port, "port", "p", 8080, "port to start service")
	rootCmd.Flags().StringVarP(&forwardURL, "forward", "f", "", "URL for forwarding requests")
	rootCmd.Flags().StringVarP(&config, "config", "c", "", "path for configuration file")
	rootCmd.Flags().BoolVarP(&printVersion, "version", "v", false, "print version and exit")
	rootCmd.Flags().StringVarP(&dataPath, "data-path", "d", ".", "path for saving cached data and config file")
	rootCmd.Execute()
}

func readConfig(path string) error {
	cfgData, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config file opening/reading error: %w", err)
	}
	cfg := make([]cfgValue, 0, 8)
	json.Unmarshal(cfgData, &cfg)
	for i, c := range cfg {
		if c.Re == "" || c.Path == "" {
			return fmt.Errorf("record #%d doesn't contain mandatory values in fields 'path' and 're'", i)
		}
		compiled, err := regexp.Compile(c.Re)
		if err != nil {
			return fmt.Errorf("compiling 're' field in record #%d error: %w", i, err)
		}
		if _, err = os.Stat(c.Path); os.IsNotExist(err) {
			return fmt.Errorf("file '%s' from 'path' of record #%d is not exists", c.Path, i)
		}
		filters = append(filters, filter{
			re:      compiled,
			path:    c.Path,
			code:    c.Code,
			stream:  c.Stream,
			headers: c.Headers,
		})
	}
	return nil
}

func root(cmd *cobra.Command, args []string) {
	if printVersion {
		fmt.Printf("http-mock v. %s\n", version)
		os.Exit(0)
	}
	mux := http.NewServeMux()
	switch {
	case forwardURL != "":
		if hosts := reURL.FindStringSubmatch(forwardURL); len(hosts) == 2 {
			forwardHost = hosts[1]
		} else {
			fmt.Printf("Error: incorrect URL: %s\n", forwardURL)
			os.Exit(1)
		}
		mux.HandleFunc("/", proxyHandler)
		fmt.Printf("Starting proxy server for %s on %s:%d\n", forwardURL, host, port)
	case config != "":
		if err := readConfig(config); err != nil {
			fmt.Printf("Error: config loading error: %s\n", err)
			os.Exit(1)
		}
		mux.HandleFunc("/", mockHandler)
		fmt.Printf("Starting MOCK server on %s:%d\n", host, port)
	default:
		fmt.Println("Error: ether -c or -f option have to be provided")
		os.Exit(1)
	}
	server := http.Server{
		Addr:    fmt.Sprintf("%s:%d", host, port),
		Handler: mux,
	}
	go func() { fmt.Println(server.ListenAndServe()) }()
	sig := make(chan (os.Signal), 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGHUP, syscall.SIGTERM)
	s := <-sig // wait for a signal
	fmt.Printf("%s received.\nStarting shutdown...", s.String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	err := server.Shutdown(ctx)
	if err != nil {
		fmt.Printf("Shutdown error:%v", err)
	}
	fmt.Println("Shutdown finished.")
	if len(cfg) > 0 {
		data, _ := json.MarshalIndent(cfg, "", "    ")
		if err := os.WriteFile(fmt.Sprintf("%s/config.json", dataPath), data, 0644); err != nil {
			fmt.Printf("Configuration writing error:%v", err)
		}
	}
}

func proxyHandler(w http.ResponseWriter, r *http.Request) {
	url := r.URL.String()
	request, err := http.NewRequest(r.Method, forwardURL+url, r.Body)
	request.Close = r.Close
	for k, v := range r.Header {
		request.Header.Add(k, strings.Join(v, " "))
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		fmt.Printf("ERROR: making the forward request error: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	fileName := fmt.Sprintf("%s/%s_response_%d.raw", dataPath, forwardHost, cnt.Next())
	headers := map[string]string{}
	for k, v := range resp.Header {
		headers[k] = strings.Join(v, " ")
	}
	if resp.Close {
		headers["Connection"] = "close"
	}
	if len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked" {
		headers["Transfer-Encoding"] = "chunked"
		tick := time.Now()
		scaner := bufio.NewScanner(resp.Body)
		file, err := os.Create(fileName)
		if err != nil {
			fmt.Printf("ERROR: creation data file error: %s", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		defer file.Close()
		sendHeaders(w, headers)
		defer startFlusher(w)()
		for scaner.Scan() {
			delay := time.Since(tick)
			tick = time.Now()
			chunk := append(scaner.Bytes(), []byte("\n")...)
			go writeChunc(file, chunk, delay)
			if _, err := w.Write(chunk); err != nil {
				fmt.Printf("ERROR: reading response body error: %s", err)
			}
		}
		go storeConfig(fileName, headers, url, resp.StatusCode, true)
	} else {
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("ERROR: reading response body ")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		go storeData(fileName, data, headers, url, resp.StatusCode)
		sendHeaders(w, headers)
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
	}
}

func sendHeaders(w http.ResponseWriter, headers map[string]string) {
	for k, v := range headers {
		w.Header().Add(k, v)
	}
}

func writeChunc(file *os.File, chunk []byte, delay time.Duration) {
	delayStr := strconv.FormatInt(delay.Milliseconds(), 10)
	spacer := strings.Repeat(" ", delaySize-len(delayStr))
	file.Write(append([]byte(spacer+delayStr+"|"), string(chunk)...))
}

func storeData(fileName string, data []byte, headers map[string]string, url string, code int) {
	err := os.WriteFile(fileName, data, 0644)
	if err != nil {
		fmt.Printf("ERROR: writing '%s' error: %s", fileName, err)
	}
	storeConfig(fileName, headers, url, code, false)
}

func storeConfig(fileName string, headers map[string]string, url string, code int, stream bool) {
	newCfg := cfgValue{
		Re:      fmt.Sprintf("^%s$", url),
		Path:    fileName,
		Code:    code,
		Headers: headers,
		Stream:  stream,
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	cfg = append(cfg, newCfg)
}

func mockHandler(w http.ResponseWriter, r *http.Request) {
	url := r.URL.String()
	found := false
	defer r.Body.Close()
	for _, c := range filters {
		if c.re.MatchString(url) {
			found = true
			sendHeaders(w, c.headers)
			if c.stream {
				defer startFlusher(w)()
				file, err := os.Open(c.path)
				if err != nil {
					readFileError(c.path, err, w, url)
					return
				}
				defer file.Close()
				scanner := bufio.NewScanner(file)
				for scanner.Scan() {
					line := scanner.Text()
					delay, err := strconv.ParseInt(strings.Trim(line[:delaySize], " "), 10, 64)
					if err != nil {
						readFileError(c.path, err, w, url)
						return
					}
					time.Sleep(time.Duration(delay) * time.Millisecond)
					_, err = w.Write([]byte(line[delaySize+1:] + "\n"))
					if err != nil {
						writeResponseError(err, w, url)
						return
					}
				}
			} else {
				data, err := os.ReadFile(c.path)
				if err != nil {
					readFileError(c.path, err, w, url)
					return
				}
				_, err = w.Write(data)
				if err != nil {
					writeResponseError(err, w, url)
					return
				}
				return
			}
		}
	}
	if !found {
		w.WriteHeader(http.StatusNotFound)
	}
}

func readFileError(path string, err error, w http.ResponseWriter, url string) {
	fmt.Printf("Error: reading file '%s' error: %v while hadlinh the %s\n", path, err, url)
	w.WriteHeader(http.StatusInternalServerError)
}

func writeResponseError(err error, w http.ResponseWriter, url string) {
	fmt.Printf("Error: sending response error: %v while hadlinh the %s\n", err, url)
	w.WriteHeader(http.StatusInternalServerError)

}

type asyncCounter struct {
	c uint64
}

func (c *asyncCounter) Next() uint64 {
	return atomic.AddUint64(&c.c, 1)
}

func startFlusher(w http.ResponseWriter) func() {
	flush := w.(http.Flusher).Flush
	ctx, cancel := context.WithCancel(context.Background())
	go func(ctx context.Context, flush func()) {
		flush()
		ticker := time.NewTicker(time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				flush()
			case <-ctx.Done():
				return
			}
		}
	}(ctx, flush)
	return cancel
}
