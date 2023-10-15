package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	delaySize            = 25
	defaultFlushInterval = 10 * time.Millisecond
)

var (
	flushInterval = defaultFlushInterval
	client        = http.DefaultClient
)

// Response is the single response storage item
type Response struct {
	URL      string            `json:"url,omitempty"`
	Body     []byte            `json:"body,omitempty"`
	Code     int               `json:"code,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Chunked  bool              `json:"chunked,omitempty"`
	Response []byte            `json:"response,omitempty"`
}

// Handler is
type Handler struct {
	host          string
	port          int
	forwardURL    string
	passthroughRe *regexp.Regexp
	urlRe         *regexp.Regexp
	bodyRe        *regexp.Regexp
	responses     map[[32]byte]Response
	server        *http.Server
	lock          sync.RWMutex
}

// Config is struct for Handler configuration
type Config struct {
	Host          string     `json:"host,omitempty"`
	Port          int        `json:"port,omitempty"`
	ForwardURL    string     `json:"forward-url,omitempty"`
	PassthroughRe string     `json:"passthrough-re,omitempty"`
	URLRe         string     `json:"url-re,omitempty"`
	BodyRe        string     `json:"body-re,omitempty"`
	Responses     []Response `json:"responses,omitempty"`
}

// NewHandler is is a single
func NewHandler(cfg []byte) (*Handler, error) {
	h := &Handler{}
	err := h.parseConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("config parsing error: %v", err)
	}
	return h, nil
}

func (h *Handler) parseConfig(data []byte) error {
	cfg := &Config{}
	err := json.Unmarshal(data, cfg)
	if err != nil {
		return fmt.Errorf("json parsing error: %v", err)
	}
	URLRe, err := regexp.Compile(cfg.URLRe)
	if err != nil {
		return fmt.Errorf("URL regexp compiling error: %v", err)
	}
	bodyRe, err := regexp.Compile(cfg.BodyRe)
	if err != nil {
		return fmt.Errorf("body regexp compiling error: %v", err)
	}
	passthroughRe, err := regexp.Compile(cfg.PassthroughRe)
	if err != nil {
		return fmt.Errorf("passthrough regexp compiling error: %v", err)
	}
	_, err = url.Parse(cfg.ForwardURL)
	if err != nil {
		return fmt.Errorf("forward URL parsing error: %v", err)
	}
	responses := make(map[[32]byte]Response, len(cfg.Responses))
	for i, r := range cfg.Responses {
		key := h.makeKey(r.URL, r.Body)
		if _, ok := responses[key]; ok {
			return fmt.Errorf("record #%d is duplicate with one of previous", i)
		}
		responses[key] = r
	}
	h.lock.Lock()
	defer h.lock.Unlock()
	h.host = cfg.Host
	h.port = cfg.Port
	h.passthroughRe = passthroughRe
	h.urlRe = URLRe
	h.bodyRe = bodyRe
	h.forwardURL = cfg.ForwardURL
	h.responses = responses
	return nil
}

// GetConfig returns Handler config
func (h *Handler) GetConfig() []byte {
	responses := make([]Response, 0, len(h.responses))
	h.lock.RLock()
	defer h.lock.RUnlock()
	for _, v := range h.responses {
		responses = append(responses, v)
	}
	c, _ := json.Marshal(Config{
		Host:          h.host,
		Port:          h.port,
		PassthroughRe: h.passthroughRe.String(),
		ForwardURL:    h.forwardURL,
		URLRe:         h.urlRe.String(),
		BodyRe:        h.bodyRe.String(),
		Responses:     responses,
	})
	return c
}

// Start starts handler
func (h *Handler) Start() error {
	h.lock.Lock()
	defer h.lock.Unlock()
	if h.server != nil {
		return errors.New("handler already started")
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", h.handle)
	h.server = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", h.host, h.port),
		Handler: mux,
	}
	go func() { logger.Println(h.server.ListenAndServe()) }()
	logger.Printf("Starting handler for %s on %s:%d\n", h.forwardURL, h.host, h.port)
	return nil
}

// Stop stops handler
func (h *Handler) Stop() error {
	h.lock.Lock()
	defer h.lock.Unlock()
	if h.server == nil {
		return errors.New("handler already stopped")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*3)
	defer cancel()
	err := h.server.Shutdown(ctx)
	if err != nil {
		return fmt.Errorf("handler for %s on %s:%d stopping error:%v", h.forwardURL, h.host, h.port, err)
	}
	logger.Printf("Handler for %s on %s:%d finished.", h.forwardURL, h.host, h.port)
	h.server = nil
	return nil
}

// SetConfig sets the handler config and restart handler in case of host/port change
func (h *Handler) SetConfig(cfg []byte) error {
	h.lock.Lock()
	pHost := h.host
	pPort := h.port
	h.lock.Unlock()
	err := h.parseConfig(cfg)
	if err != nil {
		return err
	}
	if h.port != pPort || h.host != pHost {
		h.Stop()
		h.Start()
	}
	return nil
}

func (h *Handler) makeKey(url string, body []byte) [32]byte {
	u := h.urlRe.Find([]byte(url))
	b := h.bodyRe.FindAll(body, -1)
	return sha256.Sum256(append(u, bytes.Join(b, []byte{})...))
}

func requestData(r *http.Request) (url string, headers map[string]string, body []byte, err error) {
	defer r.Body.Close()
	url = r.URL.String()
	for k, v := range r.Header {
		headers[k] = strings.Join(v, " ")
	}
	body, err = io.ReadAll(r.Body)
	if err != nil {
		return url, headers, nil, fmt.Errorf("reading body error: %s", err)
	}
	return
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	url, headers, body, err := requestData(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if h.passthroughRe.Match(append([]byte(url), body...)) {

	}
	key := h.makeKey(url, body)
	h.lock.RLock()
	response, ok := h.responses[key]
	h.lock.RUnlock()
	if ok {
		if response.Chunked {
			sendChunkedResponse(response, w)
		}
		sendSimpleResponse(response, w)
	} else {
		h.recordResponse(r.Method, url, headers, body, key, w)
	}
}

func sendSimpleResponse(response Response, w http.ResponseWriter) {
	// TO DO: implement chunked response
	w.WriteHeader(response.Code)
	body := response.Response
	for k, v := range response.Headers {
		w.Header().Add(k, v)
		if k == "Content-Encoding" && v == "gzip" {
			if compressed, err := compress(body); err != nil {
				logger.Printf("response body compression error: %v\n", err)
			} else {
				body = compressed
			}
		}
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.Write(body)
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

func (h *Handler) recordResponse(method, url string, headers map[string]string, body []byte, key [32]byte, w http.ResponseWriter) {
	request, _ := http.NewRequest(method, h.forwardURL+url, bytes.NewReader(body))
	supportCompression := false
	for k, v := range headers {
		if k == "Accept-Encoding" { // it will be automatically added by transport and the response will be automatically decompressed
			supportCompression = true
			continue
		}
		request.Header.Add(k, v)
	}
	resp, err := client.Do(request)
	if err != nil {
	}
	defer resp.Body.Close()
	headers = map[string]string{}
	for k, v := range resp.Header {
		headers[k] = strings.Join(v, ", ")
	}
	if len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked" {
		h.handleChunkedResponse(resp, url, headers, body, key, w)
	} else {
		body, err = io.ReadAll(resp.Body)
		if err != nil {
		}
		if resp.Uncompressed && supportCompression {
			headers["Content-Encoding"] = "gzip"
		}
		response := Response{
			URL:      url,
			Body:     body,
			Code:     resp.StatusCode,
			Headers:  headers,
			Chunked:  false,
			Response: body,
		}
		h.lock.Lock()
		h.responses[key] = response
		h.lock.Unlock()
		sendSimpleResponse(response, w)
	}
}

func sendChunkedResponse(response Response, w http.ResponseWriter) {
	w.WriteHeader(response.Code)
	sendHeaders(w.Header(), response.Headers)
	w.Header().Set("Transfer-Encoding", "chunked")
	scanner := bufio.NewScanner(bytes.NewReader(response.Response))
	responseWriter := NewCachedWritCloserFlusher(w, "Client response writer", w.(http.Flusher).Flush)
	defer responseWriter.Close()
	for scanner.Scan() {
		line := scanner.Text()
		delay, err := strconv.ParseInt(strings.Trim(line[:delaySize], " "), 10, 64)
		if err != nil {
			logger.Print("")
			return
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
		_, err = responseWriter.Write([]byte(line[delaySize+1:] + "\n"))
		if err != nil {
			logger.Print("")
			return
		}
	}

}

func (h *Handler) handleChunkedResponse(resp *http.Response, url string, headers map[string]string, body []byte, key [32]byte, w http.ResponseWriter) {
	tick := time.Now()
	scanner := bufio.NewScanner(resp.Body)
	respBody := bytes.NewBuffer([]byte{})
	responseWriter := NewCachedWritCloserFlusher(w, "Client response writer", w.(http.Flusher).Flush)
	defer responseWriter.Close()
	sendHeaders(w.Header(), headers)
	w.Header().Set("Transfer-Encoding", "chunked")
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
		if _, err := respBody.Write(formatChink(delay, chunk)); err != nil {
			logger.Printf("ProxyHandler: writing response to file error: %s", err)
			break
		}
	}
	response := Response{
		URL:      url,
		Body:     body,
		Code:     resp.StatusCode,
		Headers:  headers,
		Chunked:  true,
		Response: respBody.Bytes(),
	}
	h.lock.Lock()
	h.responses[key] = response
	h.lock.Unlock()
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
