package main

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	delaySize       = 25
	flushInterval   = 10 * time.Millisecond
	minCompressSize = 512
)

var client = http.DefaultClient

// Response is the single response storage item
type Response struct {
	ID       string            `json:"id,omitempty"`
	URL      string            `json:"url,omitempty"`
	Body     string            `json:"body,omitempty"`
	Code     int               `json:"code,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Chunked  bool              `json:"chunked,omitempty"`
	Response string            `json:"response,omitempty"`
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
	Host          string     `json:"host"`
	Port          int        `json:"port"`
	ForwardURL    string     `json:"forward-url"`
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

func makeRe(re string) (*regexp.Regexp, error) {
	if re != "" {
		return regexp.Compile(re)
	}
	return regexp.Compile("^$")
}

func (h *Handler) parseConfig(data []byte) error {
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("json parsing error: %v", err)
	}
	if cfg.Port == 0 {
		return fmt.Errorf("port is 0 but it have to set to value bigger than 1000")
	}
	URLRe, err := makeRe(cfg.URLRe)
	if err != nil {
		return fmt.Errorf("URL regexp compiling error: %v", err)
	}
	bodyRe, err := makeRe(cfg.BodyRe)
	if err != nil {
		return fmt.Errorf("body regexp compiling error: %v", err)
	}
	passthroughRe, err := makeRe(cfg.PassthroughRe)
	if err != nil {
		return fmt.Errorf("passthrough regexp compiling error: %v", err)
	}
	_, err = url.Parse(cfg.ForwardURL)
	if err != nil {
		return fmt.Errorf("forward URL parsing error: %v", err)
	}
	responses := make(map[[32]byte]Response, len(cfg.Responses))
	h.lock.Lock()
	defer h.lock.Unlock()
	h.host = cfg.Host
	h.port = cfg.Port
	h.passthroughRe = passthroughRe
	h.urlRe = URLRe
	h.bodyRe = bodyRe
	h.forwardURL = cfg.ForwardURL
	h.responses = responses
	for i, r := range cfg.Responses {
		key := h.makeKey(r.URL, r.Body)
		if _, ok := responses[key]; ok {
			return fmt.Errorf("record #%d is duplicate with one of previous", i)
		}
		r.ID = fmt.Sprintf("%X", key)
		responses[key] = r
	}
	return nil
}

// GetConfig returns Handler config
func (h *Handler) GetConfig() []byte {
	responses := make([]Response, 0, len(h.responses))
	h.lock.RLock()
	defer h.lock.RUnlock()
	for k, v := range h.responses {
		v.ID = fmt.Sprintf("%X", k)
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
	forMsg := ""
	if h.forwardURL != "" {
		forMsg = " for " + h.forwardURL
	}
	errCh := make(chan error, 1)
	go func() { errCh <- h.server.ListenAndServe() }()
	logger.Printf("Starting handler%s on %s:%d\n", forMsg, h.host, h.port)
	select {
	case <-time.After(100 * time.Microsecond):
		return nil
	case err := <-errCh:
		return err
	}
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
	forMsg := ""
	if h.forwardURL != "" {
		forMsg = " for " + h.forwardURL
	}
	logger.Printf("Handler%s on %s:%d finished.", forMsg, h.host, h.port)
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

// UpdateResponse updates single response.
func (h *Handler) UpdateResponse(update []byte) error {
	resp := &Response{}
	if err := json.Unmarshal(update, resp); err != nil {
		return fmt.Errorf("parsing update error: %v", err)
	}
	if resp.ID != "" {
		_ = h.DeleteResponse(resp.ID)
	}
	newKey := h.makeKey(resp.URL, resp.Body)
	if _, ok := h.responses[newKey]; !ok {
		h.lock.Lock()
		defer h.lock.Unlock()
		h.responses[h.makeKey(resp.URL, resp.Body)] = Response{
			URL:      resp.URL,
			Body:     resp.Body,
			Code:     resp.Code,
			Headers:  resp.Headers,
			Chunked:  resp.Chunked,
			Response: resp.Response,
		}
		return nil
	}
	return fmt.Errorf("update has key that overwrites other response")
}

func (h *Handler) parseKey(sKey string) (*[32]byte, error) {
	if len(sKey) != 64 {
		return nil, fmt.Errorf("update contain ID with wrong length")
	}
	key := make([]byte, 32, 32)
	_, err := hex.Decode(key, []byte(sKey))
	if err != nil {
		return nil, fmt.Errorf("parsing update.ID error: %v", err)
	}
	k := [32]byte(key)
	return &k, nil
}

// DeleteResponse deletes single response
func (h *Handler) DeleteResponse(sID string) error {
	key, err := h.parseKey(sID)
	if err != nil {
		return err
	}
	h.lock.Lock()
	delete(h.responses, *key)
	h.lock.Unlock()
	return nil
}

func (h *Handler) makeKey(url string, body string) [32]byte {
	u := h.urlRe.FindString(url)
	b := h.bodyRe.FindAllString(body, -1)
	return sha256.Sum256([]byte(u + strings.Join(b, "")))
}

func requestData(r *http.Request) (string, map[string]string, string, error) {
	defer r.Body.Close()
	url := r.URL.String()
	headers := make(map[string]string, len(r.Header))
	for k, v := range r.Header {
		headers[k] = strings.Join(v, " ")
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return url, headers, "", fmt.Errorf("reading body error: %s", err)
	}
	return url, headers, string(body), nil
}

// handle is http handler func for handler server
func (h *Handler) handle(w http.ResponseWriter, r *http.Request) {
	url, headers, body, err := requestData(r)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	key := h.makeKey(url, body)
	h.lock.RLock()
	response, ok := h.responses[key]
	h.lock.RUnlock()
	if !ok && h.forwardURL == "" {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	passthrough := h.forwardURL != "" && h.passthroughRe.Match(append([]byte(url), body...))
	if ok && !passthrough {
		if response.Chunked {
			sendChunkedResponse(response, w)
		} else {
			sendSimpleResponse(response, w)
		}
	} else {
		h.handleForward(passthrough, r.Method, url, headers, body, key, w)
	}
}

func sendSimpleResponse(response Response, w http.ResponseWriter) {
	body := []byte(response.Response)
	for k, v := range response.Headers {
		if k == "Content-Encoding" && strings.Contains(v, "gzip") && len(body) > minCompressSize {
			if compressed, err := compress(body); err != nil {
				logger.Printf("response body compression error: %v\n", err)
				continue
			} else {
				body = compressed
			}
		}
		w.Header().Set(k, v)
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(response.Code)
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

func (h *Handler) handleForward(passthrough bool, method, url string, headers map[string]string, body string, key [32]byte, w http.ResponseWriter) {
	request, _ := http.NewRequest(method, h.forwardURL+url, bytes.NewReader([]byte(body)))
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
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	headers = map[string]string{}
	for k, v := range resp.Header {
		headers[k] = strings.Join(v, ", ")
	}
	if len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked" {
		h.handleChunkedResponse(passthrough, resp, url, headers, body, key, w)
	} else {
		respBody, err := io.ReadAll(resp.Body)
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
			Response: string(respBody),
		}
		if !passthrough {
			h.lock.Lock()
			h.responses[key] = response
			h.lock.Unlock()
		}
		sendSimpleResponse(response, w)
	}
}

func sendChunkedResponse(response Response, w http.ResponseWriter) {
	sendHeaders(w.Header(), response.Headers)
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(response.Code)
	scanner := bufio.NewScanner(bytes.NewReader([]byte(response.Response)))
	responseWriter := NewCachedWritFlusher(w, "Client response writer", w.(http.Flusher).Flush, flushInterval)
	defer responseWriter.Close()
	for scanner.Scan() {
		line := scanner.Text()
		delay, err := strconv.ParseInt(strings.Trim(line[:delaySize], " "), 10, 64)
		if err != nil {
			logger.Printf("error parsing chunked file: %s\n", err)
			return
		}
		time.Sleep(time.Duration(delay) * time.Millisecond)
		_, err = responseWriter.Write([]byte(line[delaySize+1:] + "\n"))
		if err != nil {
			logger.Printf("error writing to responseWriter: %v\n", err) // todo
			return
		}
	}

}

func (h *Handler) handleChunkedResponse(passthrough bool, resp *http.Response, url string, headers map[string]string, body string, key [32]byte, w http.ResponseWriter) {
	reader := bufio.NewReader(resp.Body)
	respBody := bytes.NewBuffer([]byte{})
	sendHeaders(w.Header(), headers)
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(resp.StatusCode)
	responseWriter := NewCachedWritFlusher(w, "Client response writer", w.(http.Flusher).Flush, flushInterval)
	defer responseWriter.Close()
	tick := time.Now()
	for {
		chunk, _, err := reader.ReadLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				logger.Printf("Handler: reading response error: %s", err)
			}
			break
		}
		now := time.Now()
		delay := now.Sub(tick)
		// fmt.Printf("     %v\n", delay)
		tick = now
		// replay chunk to requester
		chunk = append(chunk, '\n')
		if _, err := responseWriter.Write(chunk); err != nil {
			logger.Printf("Handler: writing response to responseWriter error: %s", err)
			break
		}
		if !passthrough {
			if _, err := respBody.Write(formatChink(delay, chunk)); err != nil {
				logger.Printf("Handler: writing response to buffer error: %s", err)
				break
			}
		}
	}
	if passthrough {
		return
	}
	response := Response{
		URL:      url,
		Body:     body,
		Code:     resp.StatusCode,
		Headers:  headers,
		Chunked:  true,
		Response: respBody.String(),
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
