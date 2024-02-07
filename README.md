[![Go](https://github.com/slytomcat/http-mock/actions/workflows/go.yml/badge.svg)](https://github.com/slytomcat/http-mock/actions/workflows/go.yml)
[![Platform: linux-64](https://img.shields.io/badge/Platform-linux--64-blue)]()
[![Docker image](https://img.shields.io/badge/Docker-image-blue)](https://github.com/slytomcat/http-mock/pkgs/container/http-mock)

# http-mock
MOCK Service for testings http clients.

It work as service that allows to start/handle/stop the handlers for handling request to several different external services.
Each handler works on its own port, has individual configuration and can:
- record forwarded requests&responses
- replay recorded responses
- passthrough some requests as just proxy (without recording it)
- work as an recorded responses re-player (all unknown requests will return HTTP 404 status code).

The configuration of handlers can be changed via http requests to management service port. Handlers also can be deactivated (stopped) and activated (started).

Each handler can handle simple requests (with compression of the response body when it allowed into to request header) and request with multiple response parts (with `Transfer-Encoding: chunked` in the response header).

# get docker image
```
docker pull ghcr.io/slytomcat/http-mock:latest
```
Configuration of container can be done via environment variables `MANAGEMENT_HOST`, `MANAGEMENT_PORT` and `MANAGEMENT_DATA` (see [configuration](#configuration) for details).
You can also use [dockerCompose.yml](dockerCompose.yml) to start the configured container.

# get executable binary
```
curl -sL https://github.com/slytomcat/http-mock/releases/latest/download/http-mock > http-mock
chmod a+x http-mock
```
or clone/download this repository source and build it by
```
./build.sh
```
executed from the repo folder. You need Golang v.1.20 or higher to build the binary. Also the `build.sh` uses `upx` utility to compact the binary but You may skip this step by commenting or removing the line with `upc` call.

## usage

```
http-mock
```
It will start management service with default configuration on `localhost:8080` and with `_storage` as handlers config storage path.

## configuration
Command line options can be used to set configuration values or to get help/version for `http-mock`. It is help output:
```
http-mock is proxy/mock service v. master-xxxxxxxx

Usage:
  http-mock [flags]

Examples:
http-mock --port 8080 --host localhost

Values for --port, --host, --data can be also set via environment variables MANAGEMENT_PORT, MANAGEMENT_HOST and MANAGEMENT_DATA.

Flags:
  -d, --data string   path for configs storage (default "_storage")
  -h, --help          help for http-mock
  -s, --host string   host to start service (default "localhost")
  -p, --port int      port to start service (default 8080)
  -v, --version       version for http-mock
```

# management service API

## health check
- request
  - Method: `GET`
  - URL: `/`
- request example `curl "http://localhost:8080"`
- response
  - Code 200: with the service version string

## new handler
- request
  - Method: `POST`
  - URL: `/new`
  - Body: json with the [handler config](#handler-config)
- request example `curl "http://localhost:8080" -d '{"id":"my_handler","status":"active","port":8090}'`
- response
  - Code 200: json with a new handler id looking like: `{"id":"my_handler","status":"active","port":8090}`. When provided [handler config](#handler-config) contains `"status":"active"` then handler will be started.
  - Code 400: means that the provided config couldn't be parsed, the body contain the config parsing error

## start handler
- request
  - Method: `GET`
  - URL: `/start?id=<handler id>`
- request example `curl "http://localhost:8080/start?id=my_handler"`
- response
  - Code 200: without body means that the handler was successfully started
  - Code 420: means that provided handler id was not found, the body contain the error description
  - Code 500: means that the starting of the handler have been failed, the body contain the error description

## stop handler
- request
  - Method: `GET`
  - URL: `/stop?id=<handler id>`
- request example `curl "http://localhost:8080/stop?id=my_handler"`
- response
  - Code 200: without body means that the handler was successfully stopped
  - Code 420: means that provided handler id was not found, the body contain the error description
  - Code 500: means that the stopping of the handler have been failed, the body contain the error description

## get list of handlers IDs
- request
  - Method: `GET`
  - URL: `/list`
- request example `curl "http://localhost:8080/list"`
- response
  - Code 200: json list with handlers IDs and current statuses.

## get handler config
- request
  - Method: `GET`
  - URL: `/config?id=<handler id>`
- request example `curl "http://localhost:8080/config?id=my_handler"`
- response
  - Code 200: with body containing the [handler config](#handler-config)
  - Code 420: means that provided handler id was not found, the body contain the error description

## set handler config
- request
  - Method: `POST`
  - URL: `/config?id=<handler id>`
  - Body: json with the [handler config](#handler-config)
- request example `curl "http://localhost:8080/config?id=my_handler" -d '{"id":"my_handler","status":"active","port":8090}'`
- response
  - Code 200: without body means that the handler config was successfully stored
  - Code 420: means that provided handler id was not found, the body contain the error description
  - Code 400: means that the provided config couldn't be parsed, the body contain the error description

The changed value of `host` and/or `port` will restart active handler. The value of `"status"` is also will affect the handler activity status: value `"active"` will try to run or keep handler running, while value `"inactive"`(or empty value) will stop or keep handler stopped. 

## delete config
- request
  - Method: `DELETE`
  - URL: `/config?id=<handler id>`
- request example `curl -X DELETE "http://localhost:8080/config?id=my_handler"`
- response
  - Code 200: without body means that the handler config was successfully deleted
  - Code 420: means that provided handler id was not found, the body contain the error description

If handler was active (started) then before deletion it will be stopped. Note that all handler data will be lost. 

## get single response from handler config
- request
  - Method: `GET`
  - URL: `/response?id=<handler id>&resp-id=<response id>`
- request example `curl "http://localhost:8080/response?id=my_handler&resp-id=3A4CAD37B2985E4D47EC61D00A2241E3997C1AEAB9A37299217004892FEE7A3E"`
- response
  - Code 200: with body containing the requested [response](#response)
  - Code 400: means that provided response id was not found, the body contain the error description
  - Code 420: means that provided handler id was not found, the body contain the error description

## set single response from handler config
- request
  - Method: `POST`
  - URL: `/response?id=<handler id>`
  - Body: json with the [response](#response) to store in the handler config.
- request example `curl "http://localhost:8080/response?id=my_handler" -d '{"url":"/some","code":200,"response":[{"data":"ok"}]}'`

When fields `url` and `body` in the provided response are matching with fields `url` and `body` in the one of existing response, then that response will be overwritten.
If the provided response contains `id` the response with the same `id` will be deleted before storing new one. When there no response with `id` in the provided response or when `id` is not provided then the provided response will be written as new one.

- response
  - Code 200: without body means that the response was successfully stored
  - Code 400: means that provided response id was not found, the body contain the error description
  - Code 420: means that provided handler id was not found, the body contain the error description

## delete single response from handler config
- request
  - Method: `DELETE`
  - URL: `/response?id=<handler id>&resp-id=<response id>`
- response
  - Code 200: without body means that the response was successfully deleted
  - Code 400: means that provided response id was not found, the body contain the error description
  - Code 420: means that provided handler id was not found, the body contain the error description
- request example `curl -X DELETE "http://localhost:8080/?id=my_handler&resp-id=3A4CAD37B2985E4D47EC61D00A2241E3997C1AEAB9A37299217004892FEE7A3E"`

## dump all handlers configs to local folder
- request
  - Method: `GET`
  - URL: `/dump-configs`
- response
  - Code 200: without body means that all handler configs were successfully stored to local folder
- request example `curl "http://localhost:8080/dump-configs"`

## load handlers configs from local folder 
- request
  - Method: `GET`
  - URL: `/load-configs`
- response
  - Code 200: without body means that the handler configs were successfully loaded from local folder
- request example `curl "http://localhost:8080/load-configs"`

# handler config

The handler config is provided into JSON object with the following parameters (attributes):

mandatory parameters:
- `port` - port to start handler

optional parameters:
- `host` - host to start handler, by default it is "".
- `id` - any unique ID that may be provided in handler creation or while updating config. If it is not provided it is automatically created as random 16 HEX symbols. Once handler id is set all the communication with handler is performed by this `id`.
- `status` - current handler status `active` - for handler that is started and `inactive` when handler is not started. It is possible to start and stop handler by changing this config parameter via [set handler config](#set-handler-config). When a new handler created with `"status": "active"` then it will be automatically started.
- `forward-url` - where to forward requests incoming requests. If it not provided the handler can only respond on known requests.
- `passthrough-re` - regexp that used to pass through requests (without recording). This regexp have to match to the request url joined with the request body. By default it is regexp `^$` that match only empty value. The passthrough mode requires `forward-url` to be set.
- `url-re` - regexp that is applied to the request url. It can be used to specify the key part into the request url. By default it is `^.*$` that match to whole URL with parameters.
- `url-ex-re` - regexp for parts that have to be excluded form the request url that passed through `url-re` regexp. All non-overlapping parts which match this regexp will be submitted to empty string. Default: `^$` that match only empty value and it removes nothing from the request url.
- `body-re` - the regexp for request body filtering. It can be used to specify the key part into the request body. By default it is `"^.*$"` that match whole body.
- `body-ex-re` - regexp for parts that have to be excluded form the request body that passed through `body-re` regexp. All non-overlapping parts which match this regexp will be submitted on empty string. Default: `^$` that match only empty value and it removes nothing from the request body.
- `responses` - the array with recorded [responses](#respone).

## response
Each recorded response has following attributes:
- `id` - hex representation of SHA256 hash over the request's url and body (first passed through `url-re` and `body-re` filters then filtered bu `url-ex-re` and `body-ex-re`). When the response is changed (via [set single response from handler config](set-single-response-from-handler-config) or [set handler config](#set-handler-config)) and the values of `url` or/and `body` are changed it will change the `id`. Previous id will be deleted in case of change in values of `url` or/and `body`.
- `url` - original request url with parameters.
- `body` - original request body as string.
- `code` - the HTTP status code of response.
- `headers` - headers that have to be send with the response. By default the only very common heder items (like `Date` and e.t.c) will be sent.
- `response` - list of response [chunks](#chunk). For simple response it contain one chunk. Several chunks mean that response is in chunked mode.

When `response` contains more than one chunk the response header will contain `Transfer-Encoding: chunked` even if it is non set into `headers`. The `Content-Length` will not be send into headed in this case.

If `response` contains only one chunk and `Transfer-Encoding: chunked` is set into `headers` then it will be ignored (will not be send in response). The `Content-Length` will be set into the response `headers` with actual length of transferred data regardless of value set for this key into `headers` in configuration of the response.

A good idea is: never use `Transfer-Encoding: chunked` and `Content-Length` into `headers`.

### chunk
Each element of `response` list may contain:
- `data` - chunk data as string
- `delay` - number of milliseconds to wait before sending the chunk data to requester. It is ignored for only one chunk (simple) response.

The chunk with `data` and without `delay` will be send immediately. Chunk without `data` but with non-zero value of `delay` will just add waiting before processing next `chunk` without sending anything to requester. When delays into replayed response from `http-mock` is not important then it can be removed from the stored response data.

Config example:
```
{
  "ID":"my_handler",
  "status":"active",
  "host":"localhost",
  "port":8090,
  "responses":[
    {
      "id":"766C4650CB3E23432941DDCD3D663883BA3AF05C5DA44D79074657EB776B99C3",
      "url":"/url",
      "code":200,
      "response":[{"data":"ok"}]
    }
  ]
}
```

When the handler handles new request the url of request and its body is used to make the response `id`. If that `id` exists among the `responses` then the recorded response is used as response on the request. When ID is not exists then that request is forwarded to the external service using `forward-url` but if `forward-url` is not set than HTTP 404 is returned.
When `forward-url` is set then the request is forwarded and the received response will be replayed as request response and it will be stored into `responses`.
When the request match the `passthrough-re` then the forwarded request response is not stored.
