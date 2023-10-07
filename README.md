[![Go](https://github.com/slytomcat/http-mock/actions/workflows/go.yml/badge.svg)](https://github.com/slytomcat/http-mock/actions/workflows/go.yml)
# http-mock
MOCK Service for http services.

It can work in two modes:

- `proxy`: forward incoming requests to specified external service, return and store responses and create configuration for `mock` mode usage.
- `mock`: handle incoming requests from files according to the configuration file.

In `proxy` mode each request creates:
- new file file response data
- new item in configuration

A file is created in subfolder (of current path from witch the service was started) that corresponds to the forwarding URL. For example: the requests for forwarding URL: `http://www.example.com/test` will be stored in subfolder `www.example.com_test`.
The files with response data will be saved in the subfolder and will contain the rest part of request URL in its name. For example: response of request to `http://localhost:8080/path/to/end-point?id=0` will be saved to file `<request_path_parameters>_response_<n>.raw` where `n` is unique number. Config file is stored with name `config.json` in the same subfolder. It is created when service is stopped.

As each request is handled individually the several requests to the same end-point even with the same parameters will be saved in separate files and the config will have several similar records. The requests handled in the almost same time maybe written in different order. But matching of the incoming request in `mock` mode is started from the first config record up to the end. That why it is highly recommended to review and change the config file created in `proxy` mode before using it in `mock` mode.    

Responses' files may be stored in one of 2 formats:
- just raw body of response (the content of file is uncompressed even if client sent `Content-Encoding: gzip` into request header)
- special format for chunked responses (with the `Transfer-Encoding: chunked` in the response header).

The format of chunked response file is described in the section [below](#chunked-response-file-format). 

It's important to understand that responses in HTTP/2 protocol version can be treated as chunked. Thous even one chunk response in HTTP/2 protocol may be written into file with chunked data format. This is one more reason to review the generated config and correct it together with the files. The chunked file can be converted into conventional one by following bash command:
```
cut -c 27- chunked_response_3.raw > conventional_response_3.raw
```
Also change the file name in the corresponding config record and remove the `"Transfer-Encoding": "chunked"` item from `"headers"` and remove the `"streaming": "true"` parameter.

When the response is in JSON format it is a good idea to change the response file extension `.raw` to `.json`. Don't forgot to change `path` in the config file.  

## get
```
curl -sL https://github.com/slytomcat/http-mock/releases/latest/download/http-mock > http-mock
chmod a+x http-mock
```
or clone/download repository source and build it by
```
./build.sh
```
executed from the repo folder. You need Golang v.1.20 or higher to build the binary. Also the `build.sh` uses `upx` utility to compact the binary. 

## use in proxy mode

```
http-mock -f "http://example.com" 
```

## use in mock mode

```
http-mock -c config.json
```

## all options

```
Flags:
  -c, --config string      path to configuration file
  -f, --forward string     URL for forwarding requests
  -h, --help               help for http-mock
  -s, --host string        host to start service (default "localhost")
  -p, --port int           port to start service (default 8080)
  -v, --version            print version and exit
```
At least one of `-c`, `-f`, `-h` or `-v` have to be provided.

## config

The config file is JSON file with array of records (object). Each record has following parameters (attributes):

mandatory parameters:
- `re` - the regexp for incoming request path and url arguments matching.
- `path` - path to file which data will be sent as response to the request. The file with path have to exist on service start.

optional parameters:
- `body-re` - the regexp for request body matching. By default it is `"^$"` that match only empty body.
- `body-hash` - the sha256 hash-sum in HEX format that is calculated over request body or `""` if no body expected in the request. By default it is `""`.
- `headers` - headers that have to be sand with the response. By default the only very common heder items (like `Date`, `Connection`...) will be sent.
- `code` - HTTP status code to be sent with the response. By default `HTTP 200 OK` is sent.
- `stream` - indicator that file has special format and response will be send in `chunked` mode. By default:`false`.

When `stream` is `true` the response header will contain `Transfer-Encoding: chunked` even if it is non set into `headers`. 
If `stream` is equal to `false` and `Transfer-Encoding: chunked` is set into `headers` then it will be ignored (will not be send in response). A good idea is: never use `Transfer-Encoding: chunked` into `headers`.

Config example:
```
[
    {
        "re": "path\\?arg=val",
        "path": "sample_data/body_1.json",
        "headers": {
            "Connection": "keep-alive"
        }
    },
    {
        "re": "wrong$",
        "path": "/dev/null",
        "code": 400
    },
    {
        "re": "stream$",
        "path": "sample_data/stream.data",
        "headers": {
            "Transfer-Encoding": "chunked",
            "Content-Type": "text/CSV; charset=utf-8"
        },
        "stream": true
    }
]
```
When the service handles new request in `mock` mode the request is checked starting from the first record in config until it matched the record criteria. The record where match happened then used for making response. When no one config records matched the incoming request then `HTTP 402 Not found` is returned with empty response body. 

The matching criteria is: `re` match the request path with parameters AND either `body-re` match body OR `body-hash` is equal to sha256 hash-sum (in HEX format) of the request body content.

If request is maid with all parameters in the request URL (typical for GET requests) then only `re` matching works while default values for `body-re` and `body-hash` will match an empty body.
When all request parameters are sent into the request body (typical for POST requests) then config record parameters `body-re` and `body-hash` can be used for specifying the response. You can use one of it leaving another with default value (that will not match any non-empty body).

## chunked response file format
For responses in chunked mode the file have to be in special format. Example:
```
                     2000|data line #1
                     1000|data line #2
```
Each line of file contains fixed length prefix (before symbol `|`) and the chunk of data.
The prefix contains the delay in milliseconds that have to past before sending the chunk.
The `http-mock` in `proxy` mode automatically makes correct format for such files as well as correct configuration record for the replaying the response in `mock` mode.

## important
It is not necessary to restart `http-mock` working in `mock` mode if You changed the file with response, but if You changed the configuration then the restart is required.

`http-mock` working into `proxy` mode have to be stopped after recording session as it saves automatically created config file on exit.
  