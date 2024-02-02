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
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	flushInterval   = 10 * time.Millisecond
	minCompressSize = 512
)

var client = http.DefaultClient

// Chunk is a single part of response
type Chunk struct {
	Delay int    `json:"delay,omitempty"`
	Data  string `json:"data,omitempty"`
}

// Response is the single response storage item
type Response struct {
	ID       string            `json:"id,omitempty"`
	URL      string            `json:"url,omitempty"`
	Body     string            `json:"body,omitempty"`
	Code     int               `json:"code,omitempty"`
	Headers  map[string]string `json:"headers,omitempty"`
	Response []Chunk           `json:"response,omitempty"`
}

// Handler is
type Handler struct {
	id            string
	status        string
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
	ID            string
	Status        string     `json:"status"`
	Host          string     `json:"host,omitempty"`
	Port          int        `json:"port"`
	ForwardURL    string     `json:"forward-url,omitempty"`
	PassthroughRe string     `json:"passthrough-re,omitempty"`
	URLRe         string     `json:"url-re,omitempty"`
	BodyRe        string     `json:"body-re,omitempty"`
	Responses     []Response `json:"responses,omitempty"`
}

// Status represents current status of handler
type Status struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// NewHandler is is a single
func NewHandler(cfg []byte) (h *Handler, err error) {
	h = &Handler{}
	err = h.parseConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("config parsing error: %v", err)
	}
	defer func() {
		if h.status == "active" {
			err = h.Start()
		}
	}()
	if h.id != "" {
		return h, nil
	}
	idSrc, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("unexpected error while id creation: %v", err)
	}
	id := idSrc.String()
	h.id = id[len(id)-12:]
	return h, nil
}

// String return current handler status as json string
func (h *Handler) String() string {
	return fmt.Sprintf(`{"id":"%s","status":"%s"}`, h.id, h.status)
}

// GetStatus returns the reference on Status obj
func (h *Handler) GetStatus() *Status {
	h.lock.Lock()
	defer h.lock.Unlock()
	return &Status{
		ID:     h.id,
		Status: h.status,
	}
}

func makeRe(re, defaultRe string) (*regexp.Regexp, error) {
	if re != "" {
		return regexp.Compile(re)
	}
	return regexp.Compile(defaultRe)
}

func (h *Handler) parseConfig(data []byte) error {
	cfg := &Config{}
	if err := json.Unmarshal(data, cfg); err != nil {
		return fmt.Errorf("json parsing error: %v", err)
	}
	if cfg.Port < 1000 {
		return fmt.Errorf("port is %d but it have to set to value bigger than 1000", cfg.Port)
	}
	URLRe, err := makeRe(cfg.URLRe, "^.*$")
	if err != nil {
		return fmt.Errorf("URL regexp compiling error: %v", err)
	}
	bodyRe, err := makeRe(cfg.BodyRe, "^.*$")
	if err != nil {
		return fmt.Errorf("body regexp compiling error: %v", err)
	}
	passthroughRe, err := makeRe(cfg.PassthroughRe, "^$")
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
	h.id = cfg.ID
	if cfg.Status != "active" {
		h.status = "inactive"
	} else {
		h.status = cfg.Status
	}
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
	h.lock.RLock()
	defer h.lock.RUnlock()
	responses := make([]Response, 0, len(h.responses))
	for k, v := range h.responses {
		v.ID = fmt.Sprintf("%X", k)
		responses = append(responses, v)
	}
	c, _ := json.Marshal(Config{
		ID:            h.id,
		Status:        h.status,
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
	h.status = "inactive"
	if h.server != nil {
		h.status = "active"
		return fmt.Errorf("%s handler already started", h.id)
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
	logger.Printf("%s starting handler%s on %s:%d\n", h.id, forMsg, h.host, h.port)
	select {
	case <-time.After(time.Millisecond):
		h.status = "active"
		return nil
	case err := <-errCh:
		logger.Printf("%s starting error: %v\n", h.id, err)
		return err
	}
}

// Stop stops handler
func (h *Handler) Stop() error {
	h.lock.Lock()
	defer h.lock.Unlock()
	h.status = "inactive"
	forMsg := ""
	if h.forwardURL != "" {
		forMsg = " for " + h.forwardURL
	}
	if h.server == nil {
		return fmt.Errorf("%s handler already stopped", h.id)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer func() {
		cancel()
		h.server = nil
	}()
	err := h.server.Shutdown(ctx)
	if err != nil {
		h.server.Close()
		return fmt.Errorf("%s handler%s for %s on %s:%d stopping error:%v", h.id, forMsg, h.forwardURL, h.host, h.port, err)
	}
	logger.Printf("%s handler%s on %s:%d finished.", h.id, forMsg, h.host, h.port)
	return nil
}

// SetConfig sets the handler config and restart handler in case of host/port change
func (h *Handler) SetConfig(cfg []byte) error {
	h.lock.Lock()
	pHost := h.host
	pPort := h.port
	active := h.status == "active"
	h.lock.Unlock()
	err := h.parseConfig(cfg)
	if err != nil {
		return err
	}
	if (h.port != pPort || h.host != pHost || h.status == "inactive") && active {
		if err := h.Stop(); err != nil {
			return err
		}
		active = false
	}
	if h.status == "active" && !active {
		if err := h.Start(); err != nil {
			return err
		}
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

// GetResponse returns response in json format
func (h *Handler) GetResponse(sID string) ([]byte, error) {
	key, err := h.parseKey(sID)
	if err != nil {
		return nil, err
	}
	h.lock.Lock()
	defer h.lock.Unlock()
	if r, ok := h.responses[*key]; ok {
		return json.Marshal(r)
	}
	return nil, fmt.Errorf("response %s not found", sID)
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
		// reply mode
		logger.Printf("%s %s %s body='%s' replay by resp id=%s", h.id, r.Method, url, body, fmt.Sprintf("%X", key))
		if len(response.Response) > 1 {
			h.sendChunkedResponse(response, w)
		} else {
			h.sendSimpleResponse(response, w)
		}
	} else {
		// record/passthrough mode
		if passthrough {
			logger.Printf("%s %s %s body='%s' passthrough mode", h.id, r.Method, url, body)
		} else {
			logger.Printf("%s %s %s body='%s' store mode, id=%s", h.id, r.Method, url, body, fmt.Sprintf("%X", key))
		}
		h.handleForward(passthrough, r, url, headers, body, key, w)
	}
}

func (h *Handler) sendSimpleResponse(response Response, w http.ResponseWriter) {
	body := []byte{}
	if len(response.Response) > 0 {
		body = []byte(response.Response[0].Data)
	}
	for k, v := range response.Headers {
		if k == "Content-Encoding" && strings.Contains(v, "gzip") {
			if len(body) <= minCompressSize {
				continue
			}
			compressed, err := compress(body)
			if err != nil {
				logger.Printf("%s response body compression error: %v\n", h.id, err)
				continue
			}
			body = compressed
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

func (h *Handler) handleForward(passthrough bool, r *http.Request, url string, headers map[string]string, body string, key [32]byte, w http.ResponseWriter) {
	var ctx context.Context
	var cancel context.CancelFunc
	if r.Context() == nil {
		ctx, cancel = context.WithCancel(context.Background())
	} else {
		ctx, cancel = context.WithCancel(r.Context())
	}
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, r.Method, h.forwardURL+url, bytes.NewReader([]byte(body)))
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
			Response: []Chunk{{Data: string(respBody)}},
		}
		if !passthrough {
			h.lock.Lock()
			h.responses[key] = response
			h.lock.Unlock()
		}
		h.sendSimpleResponse(response, w)
	}
}

func (h *Handler) sendChunkedResponse(response Response, w http.ResponseWriter) {
	sendHeaders(w.Header(), response.Headers)
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(response.Code)
	responseWriter := NewCachedWriteFlusher(w, fmt.Sprintf("%s response writer", h.id), w.(http.Flusher).Flush, flushInterval)
	defer responseWriter.Close()
	for _, chunk := range response.Response {
		time.Sleep(time.Duration(chunk.Delay) * time.Millisecond)
		if _, err := responseWriter.Write([]byte(chunk.Data)); err != nil {
			logger.Printf("%s error writing to responseWriter: %v\n", h.id, err)
			return
		}
	}
}

func (h *Handler) handleChunkedResponse(passthrough bool, resp *http.Response, url string, headers map[string]string, body string, key [32]byte, w http.ResponseWriter) {
	reader := bufio.NewReader(resp.Body)
	sendHeaders(w.Header(), headers)
	w.Header().Set("Transfer-Encoding", "chunked")
	delimiter := true
	if w.Header().Get("Content-Type") == "application/json" {
		delimiter = false
	}
	w.WriteHeader(resp.StatusCode)
	// reader := httputil.NewChunkedReader(resp.Body) //https://paulbellamy.com/2015/06/forwarding-http-chunked-responses-without-reframing-in-go
	response := []Chunk{}
	responseWriter := NewCachedWriteFlusher(w, fmt.Sprintf("%s response writer", h.id), w.(http.Flusher).Flush, flushInterval)
	defer responseWriter.Close()
	tick := time.Now()
	for {
		chunk, _, err := reader.ReadLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				logger.Printf("%s reading response body error: %s", h.id, err)
			}
			break
		}
		now := time.Now()
		delay := now.Sub(tick)
		tick = now
		// replay chunk to requester
		if delimiter {
			chunk = append(chunk, '\n')
		}
		data := bytes.Clone(chunk) // make data clone to avoid data racing
		if _, err := responseWriter.Write(data); err != nil {
			logger.Printf("%s writing response to responseWriter error: %s", h.id, err)
			break
		}
		if !passthrough {
			response = append(response, Chunk{Delay: int(delay.Milliseconds()), Data: string(data)})
		}
	}
	if passthrough {
		return
	}
	res := Response{
		URL:      url,
		Body:     body,
		Code:     resp.StatusCode,
		Headers:  headers,
		Response: response,
	}
	h.lock.Lock()
	h.responses[key] = res
	h.lock.Unlock()
}

// write headers data to ResponseWriter header cache
func sendHeaders(w http.Header, headers map[string]string) {
	for k, v := range headers {
		w.Add(k, v)
	}
}
