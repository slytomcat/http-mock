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
	"slices"
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
	Delay      int    `json:"delay,omitempty"`
	Data       string `json:"data,omitempty"`
	compressed bool
}

func newChunk(data string, delay int) Chunk {
	c := Chunk{
		Delay: delay,
	}
	if len(data) > minCompressSize {
		cData, _ := compress([]byte(data))
		c.Data = string(cData)
		c.compressed = true
	} else {
		c.Data = data
	}
	return c
}

func (c *Chunk) getData() string {
	if c.compressed {
		data, _ := decompress([]byte(c.Data))
		return string(data)
	}
	return c.Data
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
	urlExRe       *regexp.Regexp
	bodyRe        *regexp.Regexp
	bodyExRe      *regexp.Regexp
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
	URLExRe       string     `json:"url-ex-re,omitempty"`
	BodyRe        string     `json:"body-re,omitempty"`
	BodyExRe      string     `json:"body-ex-re,omitempty"`
	Responses     []Response `json:"responses,omitempty"`
}

// Status represents current status of handler
type Status struct {
	ID     string `json:"id"`
	Status string `json:"status"`
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

func decompress(data []byte) ([]byte, error) {
	uncompressed := bytes.NewBuffer([]byte{})
	reader, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	_, err = io.Copy(uncompressed, reader)
	if err != nil {
		return nil, err
	}
	if err = reader.Close(); err != nil {
		return nil, err
	}
	return uncompressed.Bytes(), nil
}

func makeRe(re, defaultRe string) (*regexp.Regexp, error) {
	if re != "" {
		return regexp.Compile(re)
	}
	return regexp.Compile(defaultRe)
}

// NewHandler returns new handler made by provided config
func NewHandler(cfg []byte) (h *Handler, err error) {
	h = &Handler{}
	err = h.parseConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("config parsing error: %v", err)
	}
	defer func() {
		if h.status == "active" {
			err = h.Start()
			if err != nil {
				h = nil
			}
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
	URLExRe, err := makeRe(cfg.URLExRe, "^$")
	if err != nil {
		return fmt.Errorf("URL exclude regexp compiling error: %v", err)
	}
	bodyRe, err := makeRe(cfg.BodyRe, "^.*$")
	if err != nil {
		return fmt.Errorf("body regexp compiling error: %v", err)
	}
	bodyExRe, err := makeRe(cfg.BodyExRe, "^$")
	if err != nil {
		return fmt.Errorf("body exclude regexp compiling error: %v", err)
	}
	passthroughRe, err := makeRe(cfg.PassthroughRe, "^$")
	if err != nil {
		return fmt.Errorf("passthrough regexp compiling error: %v", err)
	}
	_, err = url.Parse(cfg.ForwardURL)
	if err != nil {
		return fmt.Errorf("forward URL parsing error: %v", err)
	}
	h.lock.Lock()
	defer h.lock.Unlock()
	h.id = cfg.ID
	if cfg.Status == "active" {
		h.status = "active"
	} else {
		h.status = "inactive"
	}
	h.host = cfg.Host
	h.port = cfg.Port
	h.passthroughRe = passthroughRe
	h.urlRe = URLRe
	h.urlExRe = URLExRe
	h.bodyRe = bodyRe
	h.bodyExRe = bodyExRe
	h.forwardURL = cfg.ForwardURL
	h.responses = make(map[[32]byte]Response, len(cfg.Responses))
	for i, r := range cfg.Responses {
		key := h.makeKey(r.URL, r.Body)
		if _, ok := h.responses[key]; ok {
			return fmt.Errorf("record #%d is duplicate with one of previous", i)
		}
		response := make([]Chunk, len(r.Response))
		for i, c := range r.Response {
			response[i] = newChunk(c.Data, c.Delay)
		}
		r.Response = response
		h.responses[key] = r
	}
	return nil
}

// GetConfig returns Handler config
func (h *Handler) GetConfig() ([]byte, chan []byte) {
	h.lock.RLock()
	defer h.lock.RUnlock()
	initPart, _ := json.Marshal(Config{
		ID:            h.id,
		Status:        h.status,
		Host:          h.host,
		Port:          h.port,
		PassthroughRe: h.passthroughRe.String(),
		ForwardURL:    h.forwardURL,
		URLRe:         h.urlRe.String(),
		URLExRe:       h.urlExRe.String(),
		BodyRe:        h.bodyRe.String(),
		BodyExRe:      h.bodyExRe.String(),
	})
	rest := make(chan []byte, 6)
	if len(h.responses) == 0 {
		close(rest)
		return initPart, rest
	}
	initPart, _ = bytes.CutSuffix(initPart, []byte("}"))
	initPart = append(initPart, []byte(`,"responses":[`)...)
	go func() {
		repRef := make(map[string][32]byte, len(h.responses))
		idx := make([]string, 0, len(h.responses))
		for k := range h.responses {
			sKey := fmt.Sprintf("%X", k)
			repRef[sKey] = k
			idx = append(idx, sKey)
		}
		slices.Sort(idx)
		r := bytes.NewBuffer(nil)
		for j, sKey := range idx {
			k := repRef[sKey]
			v := h.responses[k]
			p, _ := json.Marshal(Response{
				ID:      fmt.Sprintf("%X", k),
				URL:     v.URL,
				Body:    v.Body,
				Code:    v.Code,
				Headers: v.Headers,
			})
			p, _ = bytes.CutSuffix(p, []byte("}"))
			r.Write(p)
			r.WriteString(`,"response":[`)
			for i, c := range v.Response {
				r.Write([]byte(`{`))
				if c.Delay > 0 {
					r.WriteString(fmt.Sprintf(`"delay":%d`, c.Delay))
				}
				data := c.getData()
				if len(data) > 0 {
					if c.Delay > 0 {
						r.WriteString(",")
					}
					r.WriteString(`"data":"`)
					// time.Sleep(time.Millisecond)
					if len(data)+r.Len() > 4096 {
						rest <- []byte(r.String())
						r.Reset()
					}
					b, _ := json.Marshal(data)
					b = b[1 : len(b)-1]
					if len(b) >= 4096 {
						rest <- b
						r.Reset()
					} else {
						r.Write(b)
					}
					r.WriteString(`"}`)
					if i != len(v.Response)-1 {
						r.Write([]byte(`,`))
					}
				}
			}
			r.WriteString("]}")
			if j != len(h.responses)-1 {
				r.WriteString(",")
			} else {
				r.WriteString("]}")
				rest <- []byte(r.String())
			}
		}
		close(rest)
	}()
	return initPart, rest
}

func (h *Handler) makeKey(url string, body string) [32]byte {
	u := h.urlRe.FindString(url)
	ue := h.urlExRe.ReplaceAllString(u, "")
	b := strings.Join(h.bodyRe.FindAllString(body, -1), "")
	be := h.bodyExRe.ReplaceAllString(b, "")
	return sha256.Sum256([]byte(ue + be))
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
	errCh := make(chan error, 1)
	go func() { errCh <- h.server.ListenAndServe() }()
	logger.Info(h.id, "desc", "started", "addr", h.server.Addr, "forward", h.forwardURL)
	select {
	case <-time.After(time.Millisecond):
		h.status = "active"
		return nil
	case err := <-errCh:
		logger.Error(h.id, "error", err)
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
	logger.Info(h.id, "desc", "stopped", "addr", h.server.Addr, "forward", h.forwardURL)
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
	newStatus := h.status
	if (h.port != pPort || h.host != pHost || newStatus == "inactive") && active {
		if err := h.Stop(); err != nil {
			return err
		}
		active = false
	}
	if newStatus == "active" && !active {
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
		r := Response{
			URL:      resp.URL,
			Body:     resp.Body,
			Code:     resp.Code,
			Headers:  resp.Headers,
			Response: make([]Chunk, len(resp.Response)),
		}
		for i, c := range resp.Response {
			r.Response[i] = newChunk(c.Data, c.Delay)
		}
		h.responses[h.makeKey(resp.URL, resp.Body)] = r
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
		resp := Response{
			ID:       fmt.Sprintf("%X", *key),
			URL:      r.URL,
			Body:     r.Body,
			Code:     r.Code,
			Headers:  r.Headers,
			Response: make([]Chunk, len(r.Response)),
		}
		for i, c := range r.Response {
			resp.Response[i] = Chunk{
				Data:  c.getData(),
				Delay: c.Delay,
			}
		}
		return json.Marshal(resp)
	}
	return nil, fmt.Errorf("response %s not found", sID)
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
		logger.Error(h.id, "req", r.Method+url, "desc", err)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	key := h.makeKey(url, body)
	h.lock.RLock()
	response, ok := h.responses[key]
	h.lock.RUnlock()
	if !ok && h.forwardURL == "" {
		logger.Info(h.id, "req", r.Method+url, "desc", "no response found with empty forward-url")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	passthrough := h.forwardURL != "" && h.passthroughRe.Match(append([]byte(url), body...))
	if ok && !passthrough {
		// reply mode
		h.reportRequest(r.Method+url, "replay", body, key)
		if len(response.Response) > 1 {
			h.sendChunkedResponse(response, w)
		} else {
			h.sendSimpleResponse(response, w)
		}
	} else {
		// record/passthrough mode
		if passthrough {
			h.reportRequest(r.Method+url, "passthrough", body, key)
		} else {
			h.reportRequest(r.Method+url, "record", body, key)
		}
		h.handleForward(passthrough, r, url, headers, body, key, w)
	}
}

func (h *Handler) reportRequest(req, mode, body string, key [32]byte) {
	logger.Info(h.id, "req", req, "body", body, "mode", mode, "resp-id", fmt.Sprintf("%X", key))
}

func (h *Handler) sendSimpleResponse(response Response, w http.ResponseWriter) {
	body := []byte{}
	var c Chunk
	if len(response.Response) > 0 {
		c = response.Response[0]
	}
	setHeaders(w.Header(), response.Headers)
	ce := w.Header().Get("Content-Encoding")
	if strings.Contains(ce, "gzip") {
		if !c.compressed {
			body = []byte(c.Data)
			w.Header().Del("Content-Encoding")
		} else {
			body = []byte(c.Data) // must be already compressed
		}
	} else {
		if len(ce) > 0 {
			w.Header().Del("Content-Encoding")
		}
		body = []byte(c.getData())
	}
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(body)))
	w.WriteHeader(response.Code)
	w.Write(body)
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
	request, _ := http.NewRequestWithContext(ctx, r.Method, strings.TrimRight(h.forwardURL+url, "/"), bytes.NewReader([]byte(body)))
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
		logger.Error(h.id, "desc", fmt.Sprintf("resp-id: %X, request: %v, error: %v", key, request, err))
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	defer resp.Body.Close()
	headers = map[string]string{}
	for k, v := range resp.Header {
		headers[k] = strings.Join(v, ", ")
	}
	if len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked" {
		logger.Debug(h.id, "desc", "chunked")
		h.handleChunkedResponse(passthrough, resp, url, headers, body, key, w)
	} else {
		logger.Debug(h.id, "desc", "not chunked")
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
			Response: []Chunk{newChunk(string(respBody), 0)},
		}
		if !passthrough {
			h.lock.Lock()
			h.responses[key] = response
			h.lock.Unlock()
			logger.Debug(h.id, "response", fmt.Sprintf("%X", key), "desc", "stored")
		}
		h.sendSimpleResponse(response, w)
	}
}

func (h *Handler) sendChunkedResponse(response Response, w http.ResponseWriter) {
	setHeaders(w.Header(), response.Headers)
	w.Header().Set("Transfer-Encoding", "chunked")
	w.WriteHeader(response.Code)
	responseWriter := NewCachedWriteFlusher(w, fmt.Sprintf("%s response writer", h.id), w.(http.Flusher).Flush, flushInterval)
	defer responseWriter.Close()
	for _, chunk := range response.Response {
		if chunk.Delay > 0 {
			time.Sleep(time.Duration(chunk.Delay) * time.Millisecond)
		}
		if len(chunk.Data) > 0 {
			if _, err := responseWriter.Write([]byte(chunk.getData())); err != nil {
				logger.Error(h.id, "error", fmt.Sprintf("error writing to responseWriter: %v", err))
				return
			}
		}
	}
}

func (h *Handler) handleChunkedResponse(passthrough bool, resp *http.Response, url string, headers map[string]string, body string, key [32]byte, w http.ResponseWriter) {
	reader := bufio.NewReader(resp.Body)
	setHeaders(w.Header(), headers)
	w.Header().Set("Transfer-Encoding", "chunked")
	w.Header().Del("Content-Length")
	delimiter := true
	if w.Header().Get("Content-Type") == "application/json" {
		delimiter = false
	}
	w.WriteHeader(resp.StatusCode)
	response := []Chunk{}
	responseWriter := NewCachedWriteFlusher(w, fmt.Sprintf("%s response writer", h.id), w.(http.Flusher).Flush, flushInterval)
	defer responseWriter.Close()
	tick := time.Now()
	for {
		chunk, _, err := reader.ReadLine()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				logger.Error(h.id, "error", fmt.Sprintf("reading response body error:%s\n", err))
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
			logger.Error(h.id, "error", fmt.Sprintf("writing response to responseWriter error: %s", err))
			break
		}
		if !passthrough {
			response = append(response, newChunk(string(data), int(delay.Milliseconds())))
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
	logger.Info(h.id, "response", fmt.Sprintf("%X", key), "desc", "stored")
}

// write headers data to ResponseWriter header cache
func setHeaders(w http.Header, headers map[string]string) {
	for k, v := range headers {
		w.Add(k, v)
	}
}
