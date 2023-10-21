[![Go](https://github.com/slytomcat/http-mock/actions/workflows/go.yml/badge.svg)](https://github.com/slytomcat/http-mock/actions/workflows/go.yml)
# http-mock
MOCK Service for http services.

It work as service that allows to start/handle/stop the handlers for handling request to several different external services.
Each handler has it's own configuration and can:
- record the forwarded requests/responses
- replay the recorded response
- passthrough some requests as just proxy (without recording it)
- work as an recorded responses relayer (not known requests will return HTTP 404 status code).    
The configuration of service can be changed (as wel as individual responses) via http requests to management service.

Responses can be stored in one of 2 formats:
- just raw body of response (with uncompressed content of response even if the requester sent `Accept-Encoding: gzip` into request header and response has `Content-Encoding: gzip` in its header)
- special format for chunked responses (with the `Transfer-Encoding: chunked` in the response header).

The format of chunked response is described in the section [below](#chunked-response-file-format). 

It's important to understand that responses in HTTP/2 protocol version can be treated as chunked. Thous even one chunk response in HTTP/2 protocol may be written into chunked data format. This is one more reason to review the generated config and correct it together with the responses. The chunked response can be converted into conventional one by removing first 12 symbols from each line.
Also remove the `"Transfer-Encoding": "chunked"` item from `"headers"` and remove the `"chunked": "true"` parameter from corresponding response into the handler config.

## get
```
curl -sL https://github.com/slytomcat/http-mock/releases/latest/download/http-mock > http-mock
chmod a+x http-mock
```
or clone/download repository source and build it by
```
./build.sh
```
executed from the repo folder. You need Golang v.1.20 or higher to build the binary. Also the `build.sh` uses `upx` utility to compact the binary but You may skip this step by commenting or removing the line with `upc` call. 

Or you may get the docker image from github (TO-DO: make CI for docker and provide link here)

## usage

```
http-mock  
```
It will start management service on `localhost:8080`.

## options

```
Flags:
  -h, --help               help for http-mock
  -s, --host string        host to start service (default "localhost")
  -p, --port int           port to start service (default 8080)
  -v, --version            print version and exit
```
## management service API

TO-DO:


## handler config

The config is provided into JSON object wit following parameters (attributes):

mandatory parameters:
- `port` - port to start handler
- `host` - host to start handler

optional parameters:
- `id` - it random hex encoded string. It is automatically created when new handler created.
- `forward-url` - where to forward requests to handler. If it not provided the handler can only respond on known request.
- `passthrough-re` - regexp that used to pass through requests (without recording). This regexp have to match to the request url joined with the request body. By default it is regexp `^$` that match only empty value. The passthrough mode requires `forward-url` to be set.    
- `url-re` - regexp that is applied to the request url. It can be used to specify the key part into request url. By default it is `^.*$` that match to whole url (with parameters).  
- `body-re` - the regexp for request body filtering. It can be used to specify the key part into the request body. By default it is `"^.*$"` that match whole body.
- `responses` - the array with recorded responses.

Each recorded response has following attributes:
- `id` - hex representation of SHA256 hash over the request url and body (passed through `url-re` and `body-re` filters). When the response is cahnged and the new values for `url` and `body` are changed it will change the `id`.
- `url` - request url with parameters.
- `body` - request body as string.
- `code` - the HTTP status code of response.    
- `headers` - headers that have to be send with the response. By default the only very common heder items (like `Date` and  e.t.c) will be sent.
- `chunked` - indicator that response has special format and it will be send in `chunked` mode. By default:`false`.

When `chunked` is `true` the response header will contain `Transfer-Encoding: chunked` even if it is non set into `headers`. 
If `chunked` is equal to `false` and `Transfer-Encoding: chunked` is set into `headers` then it will be ignored (will not be send in response). A good idea is: never use `Transfer-Encoding: chunked` into `headers`.

Config example:
```
```
When the handler handles new request the url of request and its body is used to make the ID (sha256). If that Id exists among the `responses` then the recorded response is used as respense on the request. When ID is not exists then that request is forwarded to the external service using `forward-url`. If `forward-url` is not set than HTTP 404 is returned.
When `forward-url` is set then the request is forwarded and the received response will be replayed as request response and it will be recorded. 
When the request match the `passthrough-re` then the forwarded request response is not stored. 

## chunked response format
For responses in chunked mode the file have to be in special format. Example:
```
       2000|data line #1
       1000|data line #2
```
Each line of file contains fixed length prefix (before symbol `|`) and the chunk of data.
The prefix contains the delay in milliseconds that have to past before sending the chunk.
The handler automatically makes correct format for such responses and can replay it on following requests with the same url and body. 
