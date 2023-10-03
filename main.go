package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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
	delaySize     = 25
	flushInterval = 10 * time.Millisecond
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
	logger       = log.New(os.Stdout, "", log.LUTC|log.Lmicroseconds|log.Lshortfile)
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

func root(_ *cobra.Command, _ []string) {
	if printVersion {
		logger.Printf("http-mock v. %s\n", version)
		os.Exit(0)
	}
	mux := http.NewServeMux()
	switch {
	case forwardURL != "":
		if hosts := reURL.FindStringSubmatch(forwardURL); len(hosts) == 2 {
			forwardHost = hosts[1]
		} else {
			logger.Fatalf("incorrect URL: %s\n", forwardURL)
		}
		if err := os.MkdirAll(dataPath, 01775); err != nil {
			logger.Fatalf("creation data folder error: %s\n", err)
		}
		mux.HandleFunc("/", proxyHandler)
		logger.Printf("Starting proxy server for %s on %s:%d\n", forwardURL, host, port)
	case config != "":
		if err := readConfig(config); err != nil {
			logger.Fatalf("config loading error: %s\n", err)
		}
		mux.HandleFunc("/", mockHandler)
		logger.Printf("Starting MOCK server on %s:%d\n", host, port)
	default:
		logger.Fatalln("ether -c or -f option have to be provided")
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
	logger.Printf("\n%s received.\nStarting shutdown...", s.String())
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	err := server.Shutdown(ctx)
	if err != nil {
		logger.Printf("Shutdown error:%v", err)
	}
	logger.Println("Shutdown finished.")
	if len(cfg) > 0 { // when cfg is not empty it means that the service worked in proxy mode
		// store the config cache into file
		data, _ := json.MarshalIndent(cfg, "", "    ")
		if err := os.WriteFile(fmt.Sprintf("%s/config.json", dataPath), data, 0644); err != nil {
			logger.Printf("Configuration writing error:%v", err)
		}
	}
}

// handle requests by forwarding it to the original service and replaying and saving the response data.
// It also creates the configuration that can be used in mock mode
func proxyHandler(w http.ResponseWriter, r *http.Request) {
	url := r.URL.String()
	// make the new request to forward the original one
	request, err := http.NewRequest(r.Method, forwardURL+url, r.Body)
	request.Close = r.Close
	for k, v := range r.Header {
		request.Header.Add(k, strings.Join(v, " "))
	}
	// make forwarding request
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		logger.Printf("ERROR: making the forward request error: %s\n", err)
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
		// streamed response
		tick := time.Now()
		headers["Transfer-Encoding"] = "chunked"
		scanner := bufio.NewScanner(resp.Body)
		file, err := os.Create(fileName)
		if err != nil {
			logger.Printf("ERROR: creation data file error: %s", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fileWriter := NewCachedRecorder(file)
		responseWriter := NewCachedRecorder(NewWriteCloserFromWriter(w))
		defer fileWriter.Close()
		sendHeaders(w.Header(), headers)
		w.WriteHeader(resp.StatusCode)
		defer startFlusher(w)() // start periodically flushing the response buffer
		for scanner.Scan() {
			now := time.Now()
			chunk := append(scanner.Bytes(), '\n')
			if _, err = fileWriter.Write(formatChink(now.Sub(tick), chunk)); err != nil {
				logger.Printf("ERROR: writing response to file error: %s", err)
				break
			}
			tick = now
			// replay chunk to requester, it will be flushed by flusher
			if _, err := responseWriter.Write(chunk); err != nil {
				logger.Printf("ERROR: writing response to  error: %s", err)
				break
			}
			log.Printf("chunk handled: %s", chunk)
		}
		go storeConfig(fileName, headers, url, resp.StatusCode, true) // store config value in separate routine
	} else { // conventional response
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Printf("ERROR: reading response body ")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		go storeData(fileName, data, headers, url, resp.StatusCode) // write data to file and store config value in separate routine
		// replay response to the requestor
		sendHeaders(w.Header(), headers)
		w.WriteHeader(resp.StatusCode)
		w.Write(data)
	}

}

// write headers data to ResponseWriter header cache
func sendHeaders(w http.Header, headers map[string]string) {
	for k, v := range headers {
		w.Add(k, v)
	}
}

// format chunk data part
func formatChink(delay time.Duration, data []byte) []byte {
	delayStr := strconv.FormatInt(delay.Milliseconds(), 10)
	spacer := strings.Repeat(" ", delaySize-len(delayStr))
	return append([]byte(spacer+delayStr+"|"), string(data)...)
}

// CachedRecorder is special io.WriteCloser for cache writing
type CachedRecorder struct {
	wc     io.WriteCloser
	input  chan []byte
	closed int64
}

// NewCachedRecorder creates io.WriteCloser that pass data via channel buffer.
func NewCachedRecorder(in io.WriteCloser) io.WriteCloser {
	input := make(chan []byte, 1024)
	go func() {
		for data := range input {
			_, err := in.Write(data)
			if err != nil {
				panic(err)
			}
		}
		err := in.Close()
		if err != nil {
			panic(err)
		}
	}()
	return &CachedRecorder{
		wc:     in,
		input:  input,
		closed: 0,
	}
}

// Write chunk in to file as data line with delay and chunk data
func (c *CachedRecorder) Write(data []byte) (int, error) {
	if atomic.LoadInt64(&c.closed) != 0 {
		return 0, fmt.Errorf("writing to closed CachedWriter")
	}
	c.input <- data
	return len(data), nil
}

// Close underlying file
func (c *CachedRecorder) Close() error {
	if !atomic.CompareAndSwapInt64(&c.closed, 0, 1) {
		return fmt.Errorf("closing already closed CacheWriter")
	}
	close(c.input)
	return nil
}

// WriteCloserFromWriter is io.WriteCloser wrapper over io.Writer
type WriteCloserFromWriter struct {
	wc io.Writer
}

// NewWriteCloserFromWriter returns io.WriteCloser wrapper over io.Writer
func NewWriteCloserFromWriter(in io.Writer) io.WriteCloser {
	return &WriteCloserFromWriter{wc: in}
}

// Write io.WriteCloser implementation
func (w *WriteCloserFromWriter) Write(data []byte) (int, error) {
	return w.wc.Write(data)
}

// Close io.WriteCloser implementation
func (w *WriteCloserFromWriter) Close() error {
	return nil
}

// store data to the file and make new record in config cache
func storeData(fileName string, data []byte, headers map[string]string, url string, code int) {
	err := os.WriteFile(fileName, data, 0644)
	if err != nil {
		logger.Printf("ERROR: writing '%s' error: %s", fileName, err)
		return
	}
	storeConfig(fileName, headers, url, code, false)
}

// make new item into the config cache
func storeConfig(fileName string, headers map[string]string, url string, code int, stream bool) {
	newCfg := cfgValue{
		Re:      strings.Replace(strings.Replace(fmt.Sprintf("^%s$", url), "?", "\\?", -1), ".", "\\.", -1),
		Path:    fileName,
		Code:    code,
		Headers: headers,
		Stream:  stream,
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	cfg = append(cfg, newCfg)
}

// handle requests by replaying the responses from files according to the configuration
func mockHandler(w http.ResponseWriter, r *http.Request) {
	url := r.URL.String()
	found := false
	defer r.Body.Close()
	for _, c := range filters {
		if c.re.MatchString(url) {
			found = true
			sendHeaders(w.Header(), c.headers)
			if c.stream { // streamed response
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
			} else { // ordinal response
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
	if !found { // no config record found for the received request
		w.WriteHeader(http.StatusNotFound)
	}
}

// handle the reading file error
func readFileError(path string, err error, w http.ResponseWriter, url string) {
	logger.Printf("ERROR: reading file '%s' error: %v while handling the %s\n", path, err, url)
	w.WriteHeader(http.StatusInternalServerError)
}

// handle the writing error
func writeResponseError(err error, w http.ResponseWriter, url string) {
	logger.Printf("ERROR: sending response error: %v while handling the %s\n", err, url)
	w.WriteHeader(http.StatusInternalServerError)

}

// asynchronous counter
type asyncCounter struct {
	c uint64
}

// threads-safe function get next counter value
func (c *asyncCounter) Next() uint64 {
	return atomic.AddUint64(&c.c, 1)
}

// start periodically flushing the response buffer in separate routine and return stopping function
func startFlusher(w io.Writer) func() {
	ctx, cancel := context.WithCancel(context.Background())
	go func(ctx context.Context, flush func()) {
		flush()
		ticker := time.NewTicker(flushInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				flush()
			case <-ctx.Done():
				return
			}
		}
	}(ctx, w.(http.Flusher).Flush)
	return cancel
}
