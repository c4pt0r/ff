package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
	"time"

	"path"

	"github.com/gorilla/mux"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/sqlite"
	"github.com/ngaut/log"
)

var (
	buildIndexFlag = flag.Bool("build-index", false, "rebuild index for local file(s)")
	rmFlag         = flag.Bool("rm", false, "remove specified file and index")
	workingDir     = flag.String("dir", ".", "file dir")
	addr           = flag.String("addr", "0.0.0.0:8080", "listen addr")
	logLevel       = flag.String("L", "error", "log level")
)

var (
	db *gorm.DB
)

var (
	ErrNoSuchFile        = errors.New("no such file")
	ErrFileAlreadyExists = errors.New("file alread exists")
	ErrDBError           = errors.New("DB Error")
)

var listHtmlTpl = `
<!DOCTYPE html>
<html>
	<head>
		<meta charset="UTF-8">
		<link rel="stylesheet" href="https://unpkg.com/spectre.css/dist/spectre.min.css">
	</head>
	<body>
		<div class="container">
			<h1> Files </h1>
			<table>
			  <thead>
				<tr>
				  <th>File Link</th>
				  <th>Size</th>
				  <th>Create at</th>
				  <th>Last access</th>
				  <th>Download count</th>
				  <th></th>
				</tr>
			  </thead>
			  <tbody>
				{{range .}}
				<tr>
				  <td><a href="/f/{{ .Key }}">/f/{{ .Key }}</a></td>
				  <td>{{ .Size }}</td>
				  <td>{{ .CreateAt.Format "2006-01-02 15:04:05" }}</td>
				  <td>{{ .LastAccess.Format "2006-01-02 15:04:05" }}</td>
				  <td>{{ .DownloadCnt }}</td>
				  <td><button class="button button-outline">Delete</button></td>
				</tr>
				{{end}}
			  </tbody>
			</table>
		</div>
	</body>
</html>
`

// File Meta
type FileMeta struct {
	gorm.Model
	Key         string `gorm:"unique_index"`
	FileName    string
	CreateAt    time.Time `gorm:"index:create_at"`
	LastAccess  time.Time
	DownloadCnt int64
	Size        int64
}

func init() {
	rand.Seed(time.Now().UnixNano())
}

func randString(n int) string {
	letterRunes := []rune("abcdefghijklmnopqrstuvwxyz")
	b := make([]rune, n)
	for i := range b {
		b[i] = letterRunes[rand.Intn(len(letterRunes))]
	}
	return string(b)
}

// open DB
// TODO use another type of database
func bootstrap(dir string) error {
	log.Info("bootstraping")
	var err error
	db, err = gorm.Open("sqlite3", path.Join(dir, ".ff.db"))
	if err != nil {
		return err
	}
	db.CreateTable(&FileMeta{})
	return nil
}

func builIndexForFile(key, filepath string) error {
	f, err := os.Open(filepath)
	if err != nil {
		return err
	}

	fi, err := f.Stat()
	if err != nil {
		return err
	}

	// write file meta
	m := &FileMeta{
		Key:         key,
		FileName:    key,
		Size:        fi.Size(),
		CreateAt:    time.Now(),
		LastAccess:  time.Now(),
		DownloadCnt: 0,
	}
	if errs := db.Save(m).GetErrors(); len(errs) != 0 {
		// error occurs, retry when force flag is set.
		errs = db.Find(&FileMeta{}, "key = ?", key).Update(m).GetErrors()
		if len(errs) != 0 {
			return errs[0]
		}
	}
	return nil
}

// remove file and index
func removeFileAndIndex(key string) error {
	errs := db.Unscoped().Delete(&FileMeta{}, "key = ?", key).GetErrors()
	if len(errs) != 0 {
		// ignore index
		log.Error(errs)
	}
	// delete file
	fn := path.Join(*workingDir, key)
	err := os.Remove(fn)
	if err != nil {
		return err
	}
	return nil
}

func isValidKey(key string) bool {
	return !strings.HasPrefix(key, ".")
}

func genKey(providedKey string) string {
	if len(providedKey) != 0 && isValidKey(providedKey) {
		return providedKey
	}
	return randString(5)
}

func errResponse(w http.ResponseWriter, err error) {
	log.Error(err)
	w.WriteHeader(500)
	w.Write([]byte(err.Error()))
}

// file handlers
func fileMetaExists(key string) bool {
	return !db.Find(&FileMeta{}, "key = ?", key).RecordNotFound()
}

func getFileMeta(key string) (*FileMeta, bool) {
	meta := FileMeta{}
	if db.Find(&meta, "key = ?", key).RecordNotFound() {
		return nil, false
	}
	return &meta, true
}

func doList(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	var files []FileMeta
	var err error
	offset, limit := 0, 50
	if v := r.FormValue("offset"); len(v) > 0 {
		offset, err = strconv.Atoi(v)
	}
	if v := r.FormValue("n"); len(v) > 0 {
		limit, err = strconv.Atoi(v)
	}
	if err != nil {
		errResponse(w, err)
		return
	}
	// get file metas
	db.Order("create_at DESC").Offset(offset).Limit(limit).Find(&files)
	t, err := template.New("listPage").Parse(listHtmlTpl)
	if err != nil {
		log.Fatal(err)
	}
	t.Execute(w, files)
}

func doGet(w http.ResponseWriter, r *http.Request, key string) {
	if len(key) == 0 {
		doList(w, r)
		return
	} else if meta, ok := getFileMeta(key); ok {
		// GET /f/{key}
		// set content-length
		w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
		fn := path.Join(*workingDir, meta.Key)
		// read file
		fp, err := os.Open(fn)
		if err != nil {
			errResponse(w, err)
			return
		}
		defer fp.Close()
		_, err = io.Copy(w, fp)
		if err != nil {
			errResponse(w, err)
			return
		}
		// update last access and download count
		db.Find(&meta, "key = ?", key).
			UpdateColumn("download_cnt", gorm.Expr("download_cnt + 1")).
			UpdateColumn("last_access", time.Now())
	} else {
		w.WriteHeader(404)
	}
}

func doDelete(w http.ResponseWriter, key string) {
	if !fileMetaExists(key) {
		errResponse(w, ErrNoSuchFile)
		return
	}
	err := removeFileAndIndex(key)
	if err != nil {
		errResponse(w, err)
		return
	}
	w.Write([]byte("OK"))
}

func doPut(w http.ResponseWriter, r *http.Request, key string) {
	// check if file already exists
	// TODO let 'force' became a flag.
	force := true
	if fileMetaExists(key) && !force {
		errResponse(w, ErrFileAlreadyExists)
		return
	}
	// write file
	fn := path.Join(*workingDir, key)
	fp, err := os.OpenFile(fn, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		errResponse(w, err)
		return
	}
	defer fp.Close()
	_, err = io.Copy(fp, r.Body)
	if err != nil {
		os.Remove(fn)
		errResponse(w, err)
		return
	}
	// build index for this file
	err = builIndexForFile(key, fn)
	if err != nil {
		errResponse(w, err)
		return
	}
	w.Write([]byte("/f/" + key))
}

func fileHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	key := vars["key"]

	switch r.Method {
	case "GET":
		doGet(w, r, key)
	case "DELETE":
		doDelete(w, key)
	case "POST":
		fallthrough
	case "PUT":
		doPut(w, r, genKey(key))
	default:
		w.WriteHeader(500)
		w.Write([]byte("invalid request"))
	}
}

// http file server
func serve(addr string) error {
	r := mux.NewRouter()
	r.HandleFunc("/f", fileHandler)
	r.HandleFunc("/f/{key}", fileHandler)
	return http.ListenAndServe(addr, r)
}

func loopArgs(flg string, fn func(v string)) {
	args := os.Args[1:]
	found := false
	for _, v := range args {
		if v == flg {
			found = true
			continue
		}
		if found {
			fn(v)
		}
	}

}

func main() {
	flag.Parse()
	log.SetLevelByString(*logLevel)

	// check workingDir is valid
	if len(*workingDir) == 0 {
		log.Fatal("invalid working dir")
	}
	if stat, err := os.Stat(*workingDir); err != nil || !stat.Mode().IsDir() {
		if err != nil {
			log.Fatal(err)
		} else {
			log.Fatal("invalid working dir")
		}
	}
	// bootstrap
	if err := bootstrap(*workingDir); err != nil {
		log.Fatal(err)
	}

	// if we're just rebuilding index
	if *buildIndexFlag {
		// skip execute filename
		loopArgs("-build-index", func(v string) {
			key := filepath.Base(v)
			fmt.Println("rebuilding index for:", key, v)
			if err := builIndexForFile(key, v); err != nil {
				log.Fatal(err)
			}
		})
		return
	}

	if *rmFlag {
		loopArgs("-rm", func(v string) {
			key := filepath.Base(v)
			fmt.Println("remove file: ", key)
			if err := removeFileAndIndex(key); err != nil {
				log.Fatal(err)
			}
		})
		return
	}

	// create http server
	if err := serve(*addr); err != nil {
		log.Fatal(err)
	}
}
