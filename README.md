[![Go](https://github.com/slytomcat/http-mock/actions/workflows/go.yml/badge.svg)](https://github.com/slytomcat/http-mock/actions/workflows/go.yml)
# http-mock
MOCK Service for http services.

It work as service that allows to start/handle/stop the handlers for handling request to several different external services.
Each handler has it's own configuration and can:
- record forwarded requests&responses 
- replay recorded responses
- passthrough some requests as just proxy (without recording it)
- work as an recorded responses relayer (not known requests will return HTTP 404 status code).    
The configuration of service can be changed (as wel as individual responses) via http requests to management service.

Responses can be stored in one of 2 formats:
- just raw body of response (with uncompressed content of response even if the requester sent `Accept-Encoding: gzip` into request header and response has `Content-Encoding: gzip` in its header)
- special format for chunked responses (with the `Transfer-Encoding: chunked` in the response header).

The format of chunked response is described in the section [below](#chunked-response-file-format). 

It's important to understand that responses in HTTP/2 protocol version can be treated as chunked. Thous even one chunk response in HTTP/2 protocol may be written into chunked data format. This is one more reason to review the generated config and correct it together with the responses. The chunked response can be converted into conventional one by removing first 12 symbols from each line.
Also remove the `"Transfer-Encoding": "chunked"` item from `"headers"` and remove the `"chunked": "true"` parameter from corresponding response into the handler config.

# get docker image 
```
TO DO: provide short instructions here
```

# get executable binary
```
curl -sL https://github.com/slytomcat/http-mock/releases/latest/download/http-mock > http-mock
chmod a+x http-mock
```
or clone/download repository source and build it by
```
./build.sh
```
executed from the repo folder. You need Golang v.1.20 or higher to build the binary. Also the `build.sh` uses `upx` utility to compact the binary but You may skip this step by commenting or removing the line with `upc` call. 

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


# management service API

## new handler
- request
Method: `POST`
URL: `/new`
Body: json with the [handler config](#handler-config)
- response
Code 200: json with a new handler id looking like: `{"id": "7fd35feeb647"}`
Code 400: means that the provided config couldn't be parsed

## start handler
- request
Method: `GET`
URL: `/start?id=<handler id>`
- response
Code 200: without body means that the handler was successfully started
Code 420: means that provided handler id was not found
Code 500: means that the starting of the handler have been failed

## stop handler
- request
Method: `GET`
URL: `/stop?id=<handler id>`
- response
Code 200: without body means that the handler was successfully stopped
Code 420: means that provided handler id was not found
Code 500: means that the stopping of the handler have been failed

## get handler config
- request
Method: `GET`
URL: `/config?id=<handler id>`
- response
Code 200: with body containing the [handler config](#handler-config)
Code 420: means that provided handler id was not found

## set handler config
- request
Method: `POST`
URL: `/config?id=<handler id>`
Body: json with the [handler config](#handler-config)
- response
Code 200: without body means that the handler config was successfully stored
Code 420: means that provided handler id was not found
Code 400: means that the provided config couldn't be parsed

## get single response from handler config
- request
Method: `GET`
URL: `/response?id=<handler id>&resp-id=<response id>`
- response
Code 200: with body containing the requested [response](#response)
Code 400: means that provided response id was not found
Code 420: means that provided handler id was not found

## set single response from handler config
- request
Method: `POST`
URL: `/response?id=<handler id>`
Body: json with the [response](#response) to store in the handler config. 

When fields `url` and `body` in the provided response are matching with fields `url` and `body` in the one of existing response, then that response will be overwritten.
If the provided response contains `id` the response with the same `id` will be deleted before storing new one. When there no response with `id` in the provided response or when `id` is not provided then the provided response will be written as new one.

- response
Code 200: without body means that the response was successfully stored
Code 400: means that provided response id was not found
Code 420: means that provided handler id was not found

## delete single response from handler config
- request
Method: `DELETE`
URL: `/response?id=<handler id>&resp-id=<response id>`
- response
Code 200: without body means that the response was successfully deleted
Code 400: means that provided response id was not found
Code 420: means that provided handler id was not found

## dump all handlers config to local folder
- request
Method: `GET`
URL: `/dump-configs`
- response
Code 200: without body means that all handler configs were successfully stored to local folder

## load handler configs from local folder 
- request
Method: `GET`
URL: `/load-configs`
- response
Code 200: without body means that the handler configs were successfully loaded from local folder


# handler config

The config is provided into JSON object wit following parameters (attributes):

mandatory parameters:
- `port` - port to start handler
- `host` - host to start handler

optional parameters:
- `id` - it random hex encoded string. It is automatically created when new handler created.
- `forward-url` - where to forward requests to handler. If it not provided the handler can only respond on known request.
- `passthrough-re` - regexp that used to pass through requests (without recording). This regexp have to match to the request url joined with the request body. By default it is regexp `^$` that match only empty value. The passthrough mode requires `forward-url` to be set.    
- `url-re` - regexp that is applied to the request url. It can be used to specify the key part into the request url. By default it is `^.*$` that match to whole URL with parameters.  
- `body-re` - the regexp for request body filtering. It can be used to specify the key part into the request body. By default it is `"^.*$"` that match whole body.
- `responses` - the array with recorded [responses](#response) .

## response 
Each recorded response has following attributes:
- `id` - hex representation of SHA256 hash over the request url and body (passed through `url-re` and `body-re` filters). When the response is cahnged and the new values for `url` and `body` are changed it will change the `id`.
- `url` - request url with parameters.
- `body` - request body as string.
- `code` - the HTTP status code of response.    
- `headers` - headers that have to be send with the response. By default the only very common heder items (like `Date` and  e.t.c) will be sent.
- `chunked` - indicator that response has special format and it will be send in `chunked` mode. By default:`false`.
- `response` - response body as string.

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
