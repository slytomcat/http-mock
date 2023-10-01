# http-mock
MOCK Service for http services.

It can work in two modes:

- proxy: forward incoming requests to specified external service, return and store responses and create configuration for MOCk mode usage.
- mock: handle incoming requests from files according to the configuration file. 

## proxy mode

usage
```
http-mock -f "http://example.com" 
```
Additional you can provide the folder path where the response files and config file will be stored by using option `-d`.

## mock mode

usage
```
http-mock -c config.json
```

## all options

```
Flags:
  -c, --config string      path for configuration file
  -d, --data-path string   path for saving cached data and config file (default ".")
  -f, --forward string     URL for forwarding requests
  -h, --help               help for http-mock
  -s, --host string        host to start service (default "localhost")
  -p, --port int           port to start service (default 8080)
  -v, --version            print version and exit
```

## config

The config file is JSON file with array of items (object). Each item has following attributes:

Mandatory attributes:
- re - the regexp string. When incoming request paths and url arguments matches that regexp the this section is used for making the response.
- path - path to file which data will be sent as response to the request.

Optional attributes:
- headers - headers that have to be sand with the response.
- code - HTTP status code to be sent with the response (by default HTTP 200 is sent)
- stream - indicator that file has special format and response will be send in "chunked" mode (Transfer-Encoding: chunked)

Example:
```
[
    {
        "re": "/path\\?arg=val",
        "path": "sample_data/body_1.json",
        "headers": {
            "Connection": "keep-alive"
        }
    },
    {
        "re": "/wrong$",
        "path": "/dev/null",
        "code": 400
    }
]
```
## chunked response file format
For responses in chunked mode the file have to be in special format. Example:
```
                     2000|data line #1
                     1000|data line #2
```
Each line of file contains fixed length prefix (before symbol `|`) and the chunk data.
The prefix contains the delay in milliseconds that have to past before sending the chunk.
The `http-mock` in proxy mode automatically makes correct format for such files.

## important
It is not necessary to restart `http-mock` if You changed the file with response, but if You changed the configuration then thr restart is required. 