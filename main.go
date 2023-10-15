package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
)

var (
	version     = "local_build"
	dataDirName string
	cmdHost     string
	cmdPort     int
	logger      = log.New(os.Stdout, "", log.LUTC|log.LstdFlags|log.Lmsgprefix)
	handlers    = map[string]*Handler{}
	rootCmd     = &cobra.Command{
		Use:     "http-mock -c <config file path> | -f <URL for request forwarding>",
		Short:   fmt.Sprintf("http-mock is proxy/mock service v. %s", version),
		Run:     root,
		Example: "http-mock -c config.json -p 8000\nor\nhttp-mock -f http://examle.com",
		Version: version,
	}
)

func init() {
	rootCmd.Flags().StringVarP(&cmdHost, "host", "s", "localhost", "host to start service")
	rootCmd.Flags().IntVarP(&cmdPort, "port", "p", 8080, "port to start service")
	rootCmd.Flags().StringVarP(&dataDirName, "data", "d", "_storage", "path for data storage")
}

func main() {
	rootCmd.Execute()
}

func root(cmd *cobra.Command, _ []string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleCommands)
	server := http.Server{
		Addr:    fmt.Sprintf("%s:%d", cmdHost, cmdPort),
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
}

func handleCommands(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	switch r.Method + r.URL.Path {
	case "GET/new":
		id := ""
		handler, err := NewHandler(body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		handlers[id] = handler
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(fmt.Sprintf(`{"id": "%s"}`, id)))
	case "PATCH/start":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			if err := h.Start(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
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
	case "PUT/config":
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
	case "PATCH/stop":
		id := r.URL.Query().Get("id")
		if h, ok := handlers[id]; ok {
			if err := h.Stop(); err != nil {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// // ProxyHandler handles requests by forwarding it to the original service and replaying and saving the response data.
// // It also creates the configuration that can be used in mock mode
// func ProxyHandler(w http.ResponseWriter, r *http.Request) {
// 	url, headers, body, err := requestData(r)
// 	fileName := path.Join(dataDirName, fmt.Sprintf("%s_response_%d.raw", strings.ReplaceAll(url[1:], "/", "_"), cnt.Next()))
// 	logger.Printf("%s %s %s from %s in proxy mode, response file: %s ", r.Proto, r.Method, r.URL, r.RemoteAddr, fileName)
// 	// make the new request to forward the original one
// 	request, err := http.NewRequest(r.Method, forwardURL+url, bytes.NewReader(body))
// 	if err != nil {
// 		logger.Printf("ProxyHandler: creating request error: %s\n", err)
// 		w.WriteHeader(http.StatusInternalServerError)
// 		return
// 	}
// 	for k, v := range headers {
// 		if k == "Accept-Encoding" { // it will be automatically added by transport and the response will be automatically decompressed
// 			continue
// 		}
// 		request.Header.Add(k, v)
// 	}
// 	bodyHash := getBodyHash(body)
// 	// perform forwarding request
// 	resp, err := client.Do(request)
// 	if err != nil {
// 		logger.Printf("ProxyHandler: making the forward request error: %s\n", err)
// 		w.WriteHeader(http.StatusInternalServerError)
// 		return
// 	}
// 	defer resp.Body.Close()
// 	// extract the response header
// 	headers = map[string]string{}
// 	compressResponse := false
// 	for k, v := range resp.Header {
// 		value := strings.Join(v, ", ")
// 		if k == "Content-Encoding" && strings.Contains(value, "gzip") {
// 			compressResponse = true
// 			continue // it will be restored in case of successful compression
// 		}
// 		headers[k] = value
// 	}
// 	// fmt.Printf("DEBUG:\nheaders: %v\nresp: %+v\n", headers, resp)
// 	if len(resp.TransferEncoding) > 0 && resp.TransferEncoding[0] == "chunked" {
// 		// chunked response
// 		tick := time.Now()
// 		headers["Transfer-Encoding"] = "chunked"
// 		scanner := bufio.NewScanner(resp.Body)
// 		file, err := os.Create(fileName)
// 		if err != nil {
// 			logger.Printf("ProxyHandler: creation data file error: %s", err)
// 			w.WriteHeader(http.StatusInternalServerError)
// 			return
// 		}
// 		fileWriter := NewCachedWritCloserFlusher(file, "Response file writer", file.Close, nil)
// 		defer fileWriter.Close()
// 		responseWriter := NewCachedWritCloserFlusher(w, "Client response writer", nil, w.(http.Flusher).Flush)
// 		defer responseWriter.Close()
// 		sendHeaders(w.Header(), headers)
// 		w.WriteHeader(resp.StatusCode)
// 		for scanner.Scan() {
// 			now := time.Now()
// 			delay := now.Sub(tick)
// 			tick = now
// 			chunk := append(scanner.Bytes(), '\n')
// 			// replay chunk to requester
// 			if _, err := responseWriter.Write(chunk); err != nil {
// 				logger.Printf("ProxyHandler: writing response to responseWriter error: %s", err)
// 				break
// 			}
// 			if _, err = fileWriter.Write(formatChink(delay, chunk)); err != nil {
// 				logger.Printf("ProxyHandler: writing response to file error: %s", err)
// 				break
// 			}
// 		}
// 		go storeConfig(fileName, headers, url, resp.StatusCode, true, bodyHash) // store config value in separate routine
// 	} else { // conventional response
// 		data, err := io.ReadAll(resp.Body)
// 		if err != nil {
// 			logger.Printf("ProxyHandler: reading response body error: %v", err)
// 			w.WriteHeader(http.StatusInternalServerError)
// 			return
// 		}
// 		compressed := []byte{}
// 		if compressResponse {
// 			if compressed, err = compress(data); err != nil {
// 				logger.Printf("ProxyHandler: skipped compressing body due to error while data compression: %v", err)
// 			} else {
// 				headers["Content-Encoding"] = "gzip"
// 			}
// 		}
// 		go storeData(fileName, data, headers, url, resp.StatusCode, bodyHash) // write data to file and store config value in separate routine
// 		// replay response to the requestor
// 		sendHeaders(w.Header(), headers)
// 		w.WriteHeader(resp.StatusCode)
// 		if compressResponse && len(compressed) > 0 {
// 			w.Write(compressed)
// 		} else {
// 			w.Write(data)
// 		}
// 	}
// }

// // store data to the file and make new record in config cache
// func storeData(fileName string, data []byte, headers map[string]string, url string, code int, bodyHash string) {
// 	err := os.WriteFile(fileName, data, 0644)
// 	if err != nil {
// 		logger.Printf("storeData: writing '%s' error: %s", fileName, err)
// 		return
// 	}
// 	storeConfig(fileName, headers, url, code, false, bodyHash)
// }

// // make new item into the config cache
// func storeConfig(fileName string, headers map[string]string, url string, code int, stream bool, bodyHash string) {
// 	newCfg := cfgValue{
// 		Re:       strings.Replace(strings.Replace(fmt.Sprintf("^%s$", url), "?", "\\?", -1), ".", "\\.", -1),
// 		BodyHash: bodyHash,
// 		BodyRe:   "^$",
// 		Path:     fileName,
// 		Code:     code,
// 		Headers:  headers,
// 		Stream:   stream,
// 	}
// 	cfgLock.Lock()
// 	defer cfgLock.Unlock()
// 	cfg = append(cfg, newCfg)
// }

// func getBodyHash(body []byte) string {
// 	if len(body) > 0 {
// 		return fmt.Sprintf("%X", sha256.Sum256(body))
// 	}
// 	return ""
// }

// // MockHandler handles requests by replaying the responses from files according to the configuration
// func MockHandler(w http.ResponseWriter, r *http.Request) {
// 	url := r.URL.String()
// 	body, err := io.ReadAll(r.Body)
// 	if err != nil {
// 		logger.Printf("MockHandler: reading request body error: %s\n", err)
// 		w.WriteHeader(http.StatusInternalServerError)
// 		return
// 	}
// 	bodyHash := getBodyHash(body)
// 	found := false
// 	for _, c := range filters {
// 		if c.re.MatchString(url) && (c.bodyHash == bodyHash || c.bodyRe.Match(body)) {
// 			found = true
// 			logger.Printf("%s %s %s from %s in mock mode, response file: %s", r.Proto, r.Method, url, r.RemoteAddr, c.path)
// 			compressResponse := false
// 			for k, v := range c.headers {
// 				if k == "Content-Encoding" && strings.Contains(v, "gzip") {
// 					compressResponse = true
// 					continue // it will be restored in case of successful compression
// 				}
// 				w.Header().Add(k, v)
// 			}
// 			sendHeaders(w.Header(), c.headers)
// 			// fmt.Printf("DEBUG:\ncfg: %+v\n", c)
// 			if c.stream { // streamed response
// 				if te := w.Header().Get("Transfer-Encoding"); te != "chunked" {
// 					w.Header().Add("Transfer-Encoding", "chunked")
// 				}
// 				file, err := os.Open(c.path)
// 				if err != nil {
// 					readFileError(c.path, err, w, url)
// 					return
// 				}
// 				defer file.Close()
// 				w.WriteHeader(c.code)
// 				scanner := bufio.NewScanner(file)
// 				responseWriter := NewCachedWritCloserFlusher(w, "Client response writer", nil, w.(http.Flusher).Flush)
// 				defer responseWriter.Close()
// 				for scanner.Scan() {
// 					line := scanner.Text()
// 					delay, err := strconv.ParseInt(strings.Trim(line[:delaySize], " "), 10, 64)
// 					if err != nil {
// 						readFileError(c.path, err, w, url)
// 						return
// 					}
// 					time.Sleep(time.Duration(delay) * time.Millisecond)
// 					_, err = responseWriter.Write([]byte(line[delaySize+1:] + "\n"))
// 					if err != nil {
// 						writeResponseError(err, w, url)
// 						return
// 					}
// 				}
// 			} else { // ordinal response
// 				if te := w.Header().Get("Transfer-Encoding"); te == "chunked" {
// 					w.Header().Del("Transfer-Encoding")
// 				}
// 				data, err := os.ReadFile(c.path)
// 				w.Header().Set("Content-Length", fmt.Sprint(len(data)))
// 				if err != nil {
// 					readFileError(c.path, err, w, url)
// 					return
// 				}
// 				w.WriteHeader(c.code)
// 				if len(data) > 0 {
// 					if compressResponse {
// 						if compressed, err := compress(data); err == nil {
// 							data = compressed
// 							w.Header().Add("Content-Encoding", "gzip")
// 						}
// 					}
// 					if _, err = w.Write(data); err != nil {
// 						writeResponseError(err, w, url)
// 						return
// 					}
// 				}
// 				return
// 			}
// 		}
// 	}
// 	if !found {
// 		logger.Printf("%s %s %s from %s in mock mode, no matching records in config", r.Proto, r.Method, url, r.RemoteAddr)
// 		w.Header().Set("Content-Length", "0")
// 		w.WriteHeader(http.StatusNotFound)
// 	}
// }

// // handle the reading file error
// func readFileError(path string, err error, w http.ResponseWriter, url string) {
// 	logger.Printf("MockHandler: reading file '%s' error: %v while handling the %s\n", path, err, url)
// 	w.WriteHeader(http.StatusInternalServerError)
// }

// // handle the writing error
// func writeResponseError(err error, w http.ResponseWriter, url string) {
// 	logger.Printf("MockHandler: sending response error: %v while handling the %s\n", err, url)
// }

// // asynchronous counter
// type asyncCounter struct {
// 	c uint64
// }

// // threads-safe function get next counter value
// func (c *asyncCounter) Next() uint64 {
// 	return atomic.AddUint64(&c.c, 1)
// }
