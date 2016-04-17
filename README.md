# ff
Tiny HTTP file-sharing tool

####Install:
`go get github.com/c4pt0r/ff`

####Usage:

`ff -dir /path/to/working/dir -addr :8080`

####Example:

```
$ curl -X PUT --data-binary "@test" http://localhost:8080/f
/f/frcti

$ curl http://localhost:8080/f/frcti
hello world
```
