# ff
tiny HTTP file-sharing tool

####Install:
`go get github.com/c4pt0r/ff`

####Usage:
Server:  
`ff -dir /working/dir -addr :8080`

`curl -X PUT --data-binary "@file" http://your-site/f/<custom-key>`  
`curl -X PUT --data-binary "@file" http://your-site/f`

`curl http://your-size/f/<custom-key>`

####Example:

```
$ curl -X PUT --data-binary "@test" http://localhost:8080/f
/f/frcti

$ curl http://localhost:8080/f/frcti
hello world
```
