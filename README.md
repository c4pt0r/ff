# ff

a http file server that is suckless (it just works)  

#### Install:
`go install github.com/c4pt0r/ff@latest`

#### Usage:

`ff -dir /path/to/working/dir -addr :8080`

#### Example:

```
$ curl -X PUT --data-binary "@test" http://localhost:8080/f
/f/frcti

$ curl http://localhost:8080/f/frcti
hello world

$ curl -X DELETE http://localhost:8080/f/frcti
OK
```
