package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"mime"
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

var listHtmlTpl = `
<!DOCTYPE html>
<html>
	<head>
		<meta charset="UTF-8">
		<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/hack@0.8.1/dist/hack.css">
		<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/hack@0.8.1/dist/dark.css">
	</head>
	<body class="hack dark">
		<div class="container">
			<p>
				<b>ff - a dead simple file server, just works | Usage: <a href="https://github.com/c4pt0r/ff">github.com/c4pt0r/ff</a></b>
				<form class="form" action="/f" method="GET">
				<fieldset class="form-group">
					<input id="search" type="text" placeholder="keyword" class="form-control" name="q">
				</fieldset>
				</form>
			</p>

			<table>
			  <thead>
				<tr>
				  <th>File Link</th>
				  <th>Size</th>
				  <th>Create at</th>
				  <th>Last access</th>
				  <th>Download count</th>
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
				</tr>
				{{end}}
			  </tbody>
			</table>
		</div>
	</body>
</html>
`

var (
	buildIndexFlag = flag.Bool("build-index", false, "rebuild index for local file(s), usage: ./ff -build-index file1 file2 file3 ...")
	rmFlag         = flag.Bool("rm", false, "remove specified file and index")
	workingDir     = flag.String("dir", ".", "file dir")
	addr           = flag.String("addr", "0.0.0.0:8080", "listen addr")
	logLevel       = flag.String("L", "info", "log level")
	token          = flag.String("token", "", "token for uploading/deleting file")
	isChroot       = flag.Bool("chroot", true, "chroot")
)

var (
	// db is the database connection
	db *gorm.DB
)

var (
	mimeWhiteList = map[string]string{
		".go":   "text/plain",
		".js":   "application/javascript",
		".c":    "text/plain",
		".h":    "text/plain",
		".cpp":  "text/plain",
		".hpp":  "text/plain",
		".cc":   "text/plain",
		".hh":   "text/plain",
		".java": "text/plain",
		".py":   "text/plain",
		".rb":   "text/plain",
		".sh":   "text/plain",
		".pl":   "text/plain",
		".php":  "text/plain",
		".html": "text/html",
		".css":  "text/css",
		".ts":   "text/plain",
		".txt":  "text/plain",
	}
)

var (
	ErrNoSuchFile        = errors.New("no such file")
	ErrFileAlreadyExists = errors.New("file alread exists")
	ErrDBError           = errors.New("DB Error")
)

// FileMeta is the meta of file, it's used to store in DB(sqlite)
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
	log.Info("bootstraping...")
	var err error
	db, err = gorm.Open("sqlite3", path.Join(dir, ".ff.db"))
	if err != nil {
		return err
	}
	// is a new db
	if !db.HasTable(&FileMeta{}) {
		log.Info("create table: FileMeta")
		db.CreateTable(&FileMeta{})
	}
	log.Info("bootstrap done")
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
	var pattern string
	var err error
	offset, limit := 0, 50
	if v := r.FormValue("offset"); len(v) > 0 {
		offset, err = strconv.Atoi(v)
	}
	if v := r.FormValue("n"); len(v) > 0 {
		limit, err = strconv.Atoi(v)
	}

	if v := r.FormValue("q"); len(v) > 0 {
		pattern = v
	}

	if err != nil {
		errResponse(w, err)
		return
	}
	if pattern != "" {
		db.Where("key LIKE ?", "%"+pattern+"%").Order("create_at DESC").Offset(offset).Limit(limit).Find(&files)
	} else {
		// get file metas
		db.Order("create_at DESC").Offset(offset).Limit(limit).Find(&files)
	}
	t, err := template.New("listPage").Parse(listHtmlTpl)
	if err != nil {
		log.Fatal(err)
	}
	t.Execute(w, files)
}

func doGet(w http.ResponseWriter, r *http.Request, key string) {
	if len(key) == 0 {
		log.Info(r.RemoteAddr, "show index page")
		doList(w, r)
		return
	} else if meta, ok := getFileMeta(key); ok {
		log.Info(r.RemoteAddr, "get file:", key)
		// GET /f/{key}
		// set content-length
		w.Header().Set("Content-Length", fmt.Sprintf("%d", meta.Size))
		// set content-type
		fn := path.Join(*workingDir, meta.Key)
		// read file
		fp, err := os.Open(fn)
		if err != nil {
			errResponse(w, err)
			return
		}
		defer fp.Close()
		forceMime := r.FormValue("mime")
		if len(forceMime) > 0 {
			w.Header().Set("Content-Type", forceMime)
		} else {
			ext := path.Ext(meta.FileName)
			if tp, ok := mimeWhiteList[ext]; ok {
				w.Header().Set("Content-Type", tp)
			} else {
				w.Header().Set("Content-Type", mime.TypeByExtension(ext))
			}
		}
		// write file
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

func doDelete(w http.ResponseWriter, r *http.Request, key string) {
	log.Info(r.RemoteAddr, "delete file:", key)
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
	log.Info(r.RemoteAddr, "put file:", key)
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

func checkAuthToken(w http.ResponseWriter, r *http.Request) bool {
	authHdr := r.Header.Get("Authorization")
	if len(authHdr) == 0 {
		return false
	}
	authHdr = strings.TrimPrefix(authHdr, "Bearer ")
	if authHdr != *token {
		return false
	}
	return true
}

func fileHandler(w http.ResponseWriter, r *http.Request) {
	vars := mux.Vars(r)
	key := vars["key"]

	switch r.Method {
	case "GET":
		doGet(w, r, key)
	case "DELETE":
		if len(*token) > 0 && !checkAuthToken(w, r) {
			w.WriteHeader(401)
			return
		}
		doDelete(w, r, key)
	case "POST":
		fallthrough
	case "PUT":
		if len(*token) > 0 && !checkAuthToken(w, r) {
			w.WriteHeader(401)
			return
		}
		doPut(w, r, genKey(key))
	default:
		w.WriteHeader(500)
		w.Write([]byte("invalid request"))
	}
}

// http file server
func serve(addr string) error {
	r := mux.NewRouter()
	r.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/f", http.StatusMovedPermanently)
	})
	r.HandleFunc("/f", fileHandler)
	r.HandleFunc("/f/{key}", fileHandler)
	log.Info("listening on", addr)
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

	// chroot for security
	if *isChroot {
		if err := os.Chdir(*workingDir); err != nil {
			log.Fatal(err)
		}
		log.Info("chroot to", *workingDir)
		*workingDir = "."
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
