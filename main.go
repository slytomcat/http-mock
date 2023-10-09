package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path"
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
	re       *regexp.Regexp
	bodyRe   *regexp.Regexp
	bodyHash string
	path     string
	code     int
	headers  map[string]string
	stream   bool
}

type cfgValue struct {
	Re       string            `json:"re"`
	BodyRe   string            `json:"body-re,omitempty"`
	BodyHash string            `json:"body-hash,omitempty"`
	Path     string            `json:"path"`
	Code     int               `json:"code,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Stream   bool              `json:"stream,omitempty"`
}

const (
	delaySize            = 25
	defaultFlushInterval = 10 * time.Millisecond
)

var (
	version       = "local_build"
	forwardURL    string
	dataDirName   string
	config        string
	host          string
	port          int
	flushInterval = defaultFlushInterval
	reURL         = regexp.MustCompile(`https?://(.*)$`)
	filters       []filter
	cnt           asyncCounter
	cfg           []cfgValue
	cfgLock       sync.Mutex
	logger        = log.New(os.Stdout, "", log.LUTC|log.LstdFlags|log.Lmsgprefix)
	client        *http.Client
	rootCmd       = &cobra.Command{
		Use:     "http-mock -c <config file path> | -f <URL for request forwarding>",
		Short:   fmt.Sprintf("http-mock is proxy/mock service v. %s", version),
		Run:     root,
		Example: "http-mock -c config.json -p 8000\nor\nhttp-mock -f http://examle.com",
		Version: version,
	}
)

func init() {
	rootCmd.Flags().StringVarP(&host, "host", "s", "localhost", "host to start service")
	rootCmd.Flags().IntVarP(&port, "port", "p", 8080, "port to start service")
	rootCmd.Flags().StringVarP(&forwardURL, "forward", "f", "", "URL for forwarding requests")
	rootCmd.Flags().StringVarP(&config, "config", "c", "", "path to folder with config and responses")
}

func main() {
	rootCmd.Execute()
}

func readConfig(path string) error {
	cfgData, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("config file opening/reading error: %w", err)
	}
	cfg := make([]cfgValue, 0, 8)
	err = json.Unmarshal(cfgData, &cfg)
	if err != nil {
		return fmt.Errorf("decoding config error: %v", err)
	}
	for i, c := range cfg {
		if c.Re == "" || c.Path == "" {
			return fmt.Errorf("record #%d doesn't contain mandatory values in fields 'path' and 're'", i)
		}
		re, err := regexp.Compile(c.Re)
		if err != nil {
			return fmt.Errorf("compiling 're' field in record #%d error: %w", i, err)
		}
		if _, err = os.Stat(c.Path); os.IsNotExist(err) {
			return fmt.Errorf("file '%s' from 'path' of record #%d is not exists", c.Path, i)
		}
		var bodyRe *regexp.Regexp
		if c.BodyRe != "" {
			bodyRe, err = regexp.Compile(c.BodyRe)
			if err != nil {
				return fmt.Errorf("compiling 'body-re' field in record #%d error: %w", i, err)
			}
		} else {
			bodyRe, _ = regexp.Compile(".*")
		}
		filters = append(filters, filter{
			re:       re,
			bodyRe:   bodyRe,
			bodyHash: c.BodyHash,
			path:     c.Path,
			code:     c.Code,
			stream:   c.Stream,
			headers:  c.Headers,
		})
	}
	if len(filters) == 0 {
		return fmt.Errorf("file %s contains no data", path)
	}
	return nil
}

func root(cmd *cobra.Command, _ []string) {
	mux := http.NewServeMux()
	switch {
	case forwardURL != "":
		if hosts := reURL.FindStringSubmatch(forwardURL); len(hosts) == 2 {
			dataDirName = strings.ReplaceAll(hosts[1], "/", "_")
		} else {
			fmt.Printf("incorrect URL: %s\n", forwardURL)
			os.Exit(1)
		}
		if err := os.MkdirAll(dataDirName, 01775); err != nil {
			fmt.Printf("creation data folder '%s' error: %s\n", dataDirName, err)
			os.Exit(1)
		}
		client = http.DefaultClient
		mux.HandleFunc("/", ProxyHandler)
		logger.SetPrefix("PROXY ")
		logger.Printf("Starting proxy server for %s on %s:%d\n", forwardURL, host, port)
		break
	case config != "":
		if err := readConfig(config); err != nil {
			fmt.Printf("config loading error: %s\n", err)
			os.Exit(1)
		}
		mux.HandleFunc("/", MockHandler)
		logger.SetPrefix("MOCK ")
		logger.Printf("Starting MOCK server on %s:%d\n", host, port)
	default:
		cmd.Println("One of -c, -f, -h, or -v option have to be provided")
		cmd.Usage()
		os.Exit(1)
	}
	server := http.Server{
		Addr:    fmt.Sprintf("%s:%d", host, port),
		Handler: mux,
	}
	go func() { logger.Println(server.ListenAndServe()) }()
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
	if len(cfg) > 0 { // when cfg is not empty it means that the service worked in proxy mode
		// store the config cache into file
		data, _ := json.MarshalIndent(cfg, "", "    ")
		if err := os.WriteFile(fmt.Sprintf("%s/config.json", dataDirName), data, 0644); err != nil {
			logger.Printf("Configuration writing error:%v", err)
		}
	}
}

// ProxyHandler handles requests by forwarding it to the original service and replaying and saving the response data.
// It also creates the configuration that can be used in mock mode
func ProxyHandler(w http.ResponseWriter, r *http.Request) {
	url := r.URL.String()
	fileName := path.Join(dataDirName, fmt.Sprintf("%s_response_%d.raw", strings.ReplaceAll(url[1:], "/", "_"), cnt.Next()))
	logger.Printf("%s %s %s from %s in proxy mode, response file: %s ", r.Proto, r.Method, r.URL, r.RemoteAddr, fileName)
	// make the new request to forward the original one
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Printf("ProxyHandler: reading request body error: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	request, err := http.NewRequest(r.Method, forwardURL+url, bytes.NewReader(body))
	if err != nil {
		logger.Printf("ProxyHandler: creating request error: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	for k, v := range r.Header {
		if k == "Accept-Encoding" { // it will be automatically added by transport and the response will be automatically decompressed
			continue
		}
		request.Header.Add(k, strings.Join(v, " "))
	}
	bodyHash := getBodyHash(body)
	// perform forwarding request
	resp, err := client.Do(request)
	if err != nil {
		logger.Printf("ProxyHandler: making the forward request error: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	// extract the response header
	headers := map[string]string{}
	compressResponse := false
	for k, v := range resp.Header {
		value := strings.Join(v, ", ")
		if k == "Content-Encoding" && strings.Contains(value, "gzip") {
			compressResponse = true
			continue // it will be restored in case of successful compression
		}
		headers[k] = value
	}
	// fmt.Printf("DEBUG:\nheaders: %v\nresp: %+v\n", headers, resp)
	if len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked" {
		// chunked response
		tick := time.Now()
		headers["Transfer-Encoding"] = "chunked"
		scanner := bufio.NewScanner(resp.Body)
		file, err := os.Create(fileName)
		if err != nil {
			logger.Printf("ProxyHandler: creation data file error: %s", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fileWriter := NewCachedWritCloserFlusher(file, "Response file writer", file.Close, nil)
		defer fileWriter.Close()
		responseWriter := NewCachedWritCloserFlusher(w, "Client response writer", nil, w.(http.Flusher).Flush)
		defer responseWriter.Close()
		sendHeaders(w.Header(), headers)
		w.WriteHeader(resp.StatusCode)
		for scanner.Scan() {
			now := time.Now()
			delay := now.Sub(tick)
			tick = now
			chunk := append(scanner.Bytes(), '\n')
			// replay chunk to requester
			if _, err := responseWriter.Write(chunk); err != nil {
				logger.Printf("ProxyHandler: writing response to responseWriter error: %s", err)
				break
			}
			if _, err = fileWriter.Write(formatChink(delay, chunk)); err != nil {
				logger.Printf("ProxyHandler: writing response to file error: %s", err)
				break
			}
		}
		go storeConfig(fileName, headers, url, resp.StatusCode, true, bodyHash) // store config value in separate routine
	} else { // conventional response
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			logger.Printf("ProxyHandler: reading response body error: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		compressed := []byte{}
		if compressResponse {
			if compressed, err = compress(data); err != nil {
				logger.Printf("ProxyHandler: skipped compressing body due to error while data compression: %v", err)
			} else {
				headers["Content-Encoding"] = "gzip"
			}
		}
		go storeData(fileName, data, headers, url, resp.StatusCode, bodyHash) // write data to file and store config value in separate routine
		// replay response to the requestor
		sendHeaders(w.Header(), headers)
		w.WriteHeader(resp.StatusCode)
		if compressResponse && len(compressed) > 0 {
			w.Write(compressed)
		} else {
			w.Write(data)
		}
	}
}

func compress(data []byte) ([]byte, error) {
	compressed := bytes.NewBuffer([]byte{})
	compressor, err := gzip.NewWriterLevel(compressed, gzip.BestSpeed)
	if err != nil {
		return nil, fmt.Errorf("gzip writer creation error: %v", err)
	}
	if _, err = compressor.Write(data); err != nil {
		return nil, fmt.Errorf("writing data into gzip writer error: %v", err)
	}
	if err = compressor.Close(); err != nil {
		return nil, fmt.Errorf("closing gzip writer error: %v", err)
	}
	return compressed.Bytes(), nil
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
	spacer := "                         "[:delaySize-len(delayStr)]
	return append([]byte(spacer+delayStr+"|"), string(data)...)
}

// CachedWriteCloser is special io.WriteCloser for cache writing
type CachedWriteCloser struct {
	input  chan []byte
	closed bool
	lock   sync.Mutex
}

// NewCachedWritCloserFlusher creates io.WriteCloser that pass data via channel buffer.
func NewCachedWritCloserFlusher(in io.Writer, logPrefix string, closeFunc func() error, flushFunc func()) io.WriteCloser {
	input := make(chan []byte, 1024)
	c := &CachedWriteCloser{
		input: input,
	}
	go func() {
		ticker := time.NewTicker(flushInterval)
		if flushFunc == nil {
			ticker.Stop()
		}
		defer func() {
			if flushFunc != nil {
				ticker.Stop()
			}
			if closeFunc != nil {
				err := closeFunc()
				if err != nil {
					logger.Printf("%s: closing underlying WC error: %s", logPrefix, err)
				}
			}
		}()
		for {
			select {
			case data, ok := <-input:
				if !ok {
					return
				}
				if _, err := in.Write(data); err != nil {
					logger.Printf("%s: writing to underlying WC error: %s", logPrefix, err)
					c.lock.Lock()
					c.closed = true
					close(input)
					c.lock.Unlock()
					for range input {
					} // drain input
					if flushFunc != nil {
						ticker.Stop()
						for len(ticker.C) > 0 {
							<-ticker.C
						}
						flushFunc = nil
					}
					return
				}
			case <-ticker.C:
				flushFunc()
			}
		}
	}()
	return c
}

// Write chunk in to file as data line with delay and chunk data
func (c *CachedWriteCloser) Write(data []byte) (int, error) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return 0, fmt.Errorf("writing to closed CachedWriter")
	}
	c.input <- data
	return len(data), nil
}

// Close underlying file
func (c *CachedWriteCloser) Close() error {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.closed {
		return fmt.Errorf("closing already closed CacheWriter")
	}
	c.closed = true
	close(c.input)
	return nil
}

// store data to the file and make new record in config cache
func storeData(fileName string, data []byte, headers map[string]string, url string, code int, bodyHash string) {
	err := os.WriteFile(fileName, data, 0644)
	if err != nil {
		logger.Printf("storeData: writing '%s' error: %s", fileName, err)
		return
	}
	storeConfig(fileName, headers, url, code, false, bodyHash)
}

// make new item into the config cache
func storeConfig(fileName string, headers map[string]string, url string, code int, stream bool, bodyHash string) {
	newCfg := cfgValue{
		Re:       strings.Replace(strings.Replace(fmt.Sprintf("^%s$", url), "?", "\\?", -1), ".", "\\.", -1),
		BodyHash: bodyHash,
		BodyRe:   "^$",
		Path:     fileName,
		Code:     code,
		Headers:  headers,
		Stream:   stream,
	}
	cfgLock.Lock()
	defer cfgLock.Unlock()
	cfg = append(cfg, newCfg)
}

func getBodyHash(body []byte) string {
	if len(body) > 0 {
		return fmt.Sprintf("%X", sha256.Sum256(body))
	}
	return ""
}

// MockHandler handles requests by replaying the responses from files according to the configuration
func MockHandler(w http.ResponseWriter, r *http.Request) {
	url := r.URL.String()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Printf("MockHandler: reading request body error: %s\n", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	bodyHash := getBodyHash(body)
	found := false
	for _, c := range filters {
		if c.re.MatchString(url) && (c.bodyHash == bodyHash || c.bodyRe.Match(body)) {
			found = true
			logger.Printf("%s %s %s from %s in mock mode, response file: %s", r.Proto, r.Method, url, r.RemoteAddr, c.path)
			compressResponse := false
			for k, v := range c.headers {
				if k == "Content-Encoding" && strings.Contains(v, "gzip") {
					compressResponse = true
					continue // it will be restored in case of successful compression
				}
				w.Header().Add(k, v)
			}
			sendHeaders(w.Header(), c.headers)
			// fmt.Printf("DEBUG:\ncfg: %+v\n", c)
			if c.stream { // streamed response
				if te := w.Header().Get("Transfer-Encoding"); te != "chunked" {
					w.Header().Add("Transfer-Encoding", "chunked")
				}
				file, err := os.Open(c.path)
				if err != nil {
					readFileError(c.path, err, w, url)
					return
				}
				defer file.Close()
				w.WriteHeader(c.code)
				scanner := bufio.NewScanner(file)
				responseWriter := NewCachedWritCloserFlusher(w, "Client response writer", nil, w.(http.Flusher).Flush)
				defer responseWriter.Close()
				for scanner.Scan() {
					line := scanner.Text()
					delay, err := strconv.ParseInt(strings.Trim(line[:delaySize], " "), 10, 64)
					if err != nil {
						readFileError(c.path, err, w, url)
						return
					}
					time.Sleep(time.Duration(delay) * time.Millisecond)
					_, err = responseWriter.Write([]byte(line[delaySize+1:] + "\n"))
					if err != nil {
						writeResponseError(err, w, url)
						return
					}
				}
			} else { // ordinal response
				if te := w.Header().Get("Transfer-Encoding"); te == "chunked" {
					w.Header().Del("Transfer-Encoding")
				}
				data, err := os.ReadFile(c.path)
				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
				if err != nil {
					readFileError(c.path, err, w, url)
					return
				}
				w.WriteHeader(c.code)
				if len(data) > 0 {
					if compressResponse {
						if compressed, err := compress(data); err == nil {
							data = compressed
							w.Header().Add("Content-Encoding", "gzip")
						}
					}
					if _, err = w.Write(data); err != nil {
						writeResponseError(err, w, url)
						return
					}
				}
				return
			}
		}
	}
	if !found {
		logger.Printf("%s %s %s from %s in mock mode, no matching records in config", r.Proto, r.Method, url, r.RemoteAddr)
		w.Header().Set("Content-Length", "0")
		w.WriteHeader(http.StatusNotFound)
	}
}

// handle the reading file error
func readFileError(path string, err error, w http.ResponseWriter, url string) {
	logger.Printf("MockHandler: reading file '%s' error: %v while handling the %s\n", path, err, url)
	w.WriteHeader(http.StatusInternalServerError)
}

// handle the writing error
func writeResponseError(err error, w http.ResponseWriter, url string) {
	logger.Printf("MockHandler: sending response error: %v while handling the %s\n", err, url)
}

// asynchronous counter
type asyncCounter struct {
	c uint64
}

// threads-safe function get next counter value
func (c *asyncCounter) Next() uint64 {
	return atomic.AddUint64(&c.c, 1)
}
