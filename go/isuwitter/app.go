package main

import (
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"html/template"
	"log"
	"net"
	"net/http"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	//"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/walf443/go-sql-tracer"

	"github.com/gorilla/mux"
	"github.com/gorilla/sessions"
	"github.com/unrolled/render"
)

type Tweet struct {
	ID        int
	UserID    int
	Text      string
	CreatedAt time.Time

	UserName string
	HTML     string
	Time     string
}

type User struct {
	ID       int
	Name     string
	Salt     string
	Password string
}

const (
	sessionName     = "isuwitter_session"
	sessionSecret   = "isuwitter"
	perPage         = 50
	isutomoEndpoint = "http://localhost:8081"
)

var (
	re             *render.Render
	store          *sessions.FilesystemStore
	db             *sql.DB
	errInvalidUser = errors.New("Invalid User")

	dbTomo          *sql.DB
	userNameMap     map[string]string
	userNameMapLock sync.RWMutex
	fCache          *cacheFriends
	hport           int
)

func init() {
	flag.IntVar(&hport, "port", 0, "port to listen")
	flag.Parse()

	userNameMap = make(map[string]string, 0)

	fCache = NewCacheFriends()
}

func getuserID(name string) int {
	row := db.QueryRow(`SELECT id FROM users WHERE name = ?`, name)
	user := User{}
	err := row.Scan(&user.ID)
	if err != nil {
		return 0
	}
	return user.ID
}

func fillUserNames(tweets []*Tweet) error {
	userIds := make([]string, 0)

	userNameMapLock.RLock()
	for _, tweet := range tweets {
		id := strconv.Itoa(tweet.UserID)
		name, ok := userNameMap[id]
		if ok {
			tweet.UserName = name
		} else {
			userIds = append(userIds, id)
		}
	}
	userNameMapLock.RUnlock()

	if len(userIds) > 0 {
		placeholder := strings.Join(userIds, ",")
		rows, err := db.Query(fmt.Sprintf(`SELECT id, name FROM users WHERE id IN (%s)`, placeholder))
		if err != nil {
			panic(err)
			return err
		}

		userNameMapLock.Lock()
		for rows.Next() {
			var id string
			var name string
			err := rows.Scan(&id, &name)
			if err != nil {
				return err
			}
			userNameMap[id] = name
		}
		userNameMapLock.Unlock()

		userNameMapLock.RLock()
		for _, tweet := range tweets {
			name, ok := userNameMap[strconv.Itoa(tweet.UserID)]
			if ok {
				tweet.UserName = name
			}
		}
		userNameMapLock.RUnlock()
	}

	return nil
}

func getUserName(id int) string {
	row := db.QueryRow(`SELECT name FROM users WHERE id = ?`, id)
	user := User{}
	err := row.Scan(&user.Name)
	if err != nil {
		return ""
	}
	return user.Name
}

func htmlify(tweet string) string {
	tweet = strings.Replace(tweet, "&", "&amp;", -1)
	tweet = strings.Replace(tweet, "<", "&lt;", -1)
	tweet = strings.Replace(tweet, ">", "&gt;", -1)
	tweet = strings.Replace(tweet, "'", "&apos;", -1)
	tweet = strings.Replace(tweet, "\"", "&quot;", -1)
	re := regexp.MustCompile("#(\\S+)(\\s|$)")
	tweet = re.ReplaceAllStringFunc(tweet, func(tag string) string {
		return fmt.Sprintf("<a class=\"hashtag\" href=\"/hashtag/%s\">#%s</a>", tag[1:len(tag)], html.EscapeString(tag[1:len(tag)]))
	})
	return tweet
}

type cacheFriends struct {
	// Setが多いならsync.Mutex
	sync.RWMutex
	items map[string][]string
}

func NewCacheFriends() *cacheFriends {
	m := make(map[string][]string)
	c := &cacheFriends{
		items: m,
	}
	return c
}

func (c *cacheFriends) Set(name string, value []string) {
	c.Lock()
	c.items[name] = value
	c.Unlock()
}

func (c *cacheFriends) Del(name string, value string) {
	c.Lock()
	defer c.Unlock()
	v, found := c.items[name]
	if !found {
		return
	}

	target := -1
	for i, id := range v {
		if id == value {
			target = i
			break
		}
	}

	if target == -1 {
		return
	}

	v[target] = v[len(v)-1]
	c.items[name] = v[:len(v)-1]
}

func (c *cacheFriends) Add(name string, value string) {
	c.Lock()
	defer c.Unlock()
	v, found := c.items[name]
	if !found {
		return
	}

	c.items[name] = append(v, value)
}

func (c *cacheFriends) Get(name string) ([]string, bool) {
	c.RLock()
	v, found := c.items[name]
	c.RUnlock()

	return v, found
}

func loadFriends(name string) ([]string, error) {
	v, found := fCache.Get(name)
	if found {
		return v, nil
	}

	result, err := _loadFriends(name)
	if err != nil {
		return result, err
	}

	fCache.Set(name, result)

	return result, err
}

func _loadFriends(name string) ([]string, error) {
	resp, err := http.DefaultClient.Get(pathURIEscape(fmt.Sprintf("%s/%s", isutomoEndpoint, name)))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var data struct {
		Result []string `json:"friends"`
	}
	err = json.NewDecoder(resp.Body).Decode(&data)
	return data.Result, err
}

func initializeHandler(w http.ResponseWriter, r *http.Request) {
	_, err := db.Exec(`DELETE FROM tweets WHERE id > 100000`)
	if err != nil {
		badRequest(w)
		return
	}

	_, err = db.Exec(`DELETE FROM users WHERE id > 1000`)
	if err != nil {
		badRequest(w)
		return
	}

	userNameMapLock.Lock()
	userNameMap = make(map[string]string, 0)
	userNameMapLock.Unlock()
	err = initUserNameMap()
	if err != nil {
		badRequest(w)
		return
	}

	path, err := exec.LookPath("mysql")
	if err != nil {
		panic(err)
		return
	}

	exec.Command(path, "-u", "root", "-D", "isutomo", "<", "../../sql/seed_isutomo2.sql").Run()
	if err != nil {
		panic(err)
		return
	}

	fCache = NewCacheFriends()

	rows, err := dbTomo.Query("SELECT `me`, `friends` FROM friends")
	if err != nil && err != sql.ErrNoRows {
		panic(err)
	}

	for rows.Next() {
		var me string
		var friends string
		err := rows.Scan(&me, &friends)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			return
		}

		fCache.Set(me, strings.Split(friends, ","))
	}

	re.JSON(w, http.StatusOK, map[string]string{"result": "ok"})
}

func initUserNameMap() error {
	rows, err := db.Query(fmt.Sprintf("SELECT `id`, `name` FROM users"))
	if err != nil {
		return err
	}

	userNameMapLock.Lock()
	for rows.Next() {
		var id, name string
		err := rows.Scan(&id, &name)
		if err != nil {
			return err
		}
		userNameMap[id] = name
	}
	userNameMapLock.Unlock()

	return nil
}

func topHandler(w http.ResponseWriter, r *http.Request) {
	var name string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(userID.(int))
	}

	if name == "" {
		flush, _ := session.Values["flush"].(string)
		session := getSession(w, r)
		session.Options = &sessions.Options{MaxAge: -1}
		session.Save(r, w)

		re.HTML(w, http.StatusOK, "index", struct {
			Name  string
			Flush string
		}{
			name,
			flush,
		})
		return
	}

	result, err := loadFriends(name)
	if err != nil {
		panic(err)
		badRequest(w)
		return
	}

	names := strings.Join(result, `","`)

	var rows *sql.Rows

	rows, err = db.Query(fmt.Sprintf("SELECT `id` FROM users WHERE `name` IN (\"%s\") ORDER BY id ASC", names))
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		return
	}
	defer rows.Close()

	userIDs := make([]string, 0, len(names))

	for rows.Next() {
		var id int64
		err := rows.Scan(&id)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			return
		}

		userIDs = append(userIDs, strconv.FormatInt(id, 10))
	}

	sqlParts := strings.Join(userIDs, ",")

	until := r.URL.Query().Get("until")
	if until == "" {
		rows, err = db.Query(fmt.Sprintf("SELECT * FROM tweets FORCE INDEX (PRIMARY) WHERE `user_id` IN (%s) ORDER BY id DESC LIMIT ?", sqlParts), perPage)
	} else {
		rows, err = db.Query(fmt.Sprintf("SELECT * FROM tweets FORCE INDEX (PRIMARY) WHERE `user_id` IN (%s) AND created_at < ? ORDER BY id DESC LIMIT ?", sqlParts), until, perPage)
	}

	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		return
	}
	defer rows.Close()

	tweets := make([]*Tweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.ID, &t.UserID, &t.Text, &t.CreatedAt)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			return
		}
		t.HTML = htmlify(t.Text)
		t.Time = t.CreatedAt.Format("2006-01-02 15:04:05")

		tweets = append(tweets, &t)
		if len(tweets) == perPage {
			break
		}
	}

	err = fillUserNames(tweets)
	if err != nil {
		badRequest(w)
		return
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*Tweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "index", struct {
		Name   string
		Tweets []*Tweet
	}{
		name, tweets,
	})
}

func tweetPostHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		u := getUserName(userID.(int))
		if u == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
	} else {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	text := r.FormValue("text")
	if text == "" {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	_, err := db.Exec(`INSERT INTO tweets (user_id, text, created_at) VALUES (?, ?, NOW())`, userID, text)
	if err != nil {
		badRequest(w)
		return
	}

	http.Redirect(w, r, "/", http.StatusFound)
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	row := db.QueryRow(`SELECT * FROM users WHERE name = ?`, name)
	user := User{}
	err := row.Scan(&user.ID, &user.Name, &user.Salt, &user.Password)
	if err != nil && err != sql.ErrNoRows {
		http.NotFound(w, r)
		return
	}
	if err == sql.ErrNoRows || user.Password != fmt.Sprintf("%x", sha1.Sum([]byte(user.Salt+r.FormValue("password")))) {
		session := getSession(w, r)
		session.Values["flush"] = "ログインエラー"
		session.Save(r, w)
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	session := getSession(w, r)
	session.Values["user_id"] = user.ID
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	session := getSession(w, r)
	session.Options = &sessions.Options{MaxAge: -1}
	session.Save(r, w)
	http.Redirect(w, r, "/", http.StatusFound)
}

func followHandler(w http.ResponseWriter, r *http.Request) {
	var userName string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		u := getUserName(userID.(int))
		if u == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		userName = u
	} else {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	targetUserName := r.FormValue("user")

	fCache.Add(userName, targetUserName)

	http.Redirect(w, r, "/", http.StatusFound)
}

func unfollowHandler(w http.ResponseWriter, r *http.Request) {
	var userName string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		u := getUserName(userID.(int))
		if u == "" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		userName = u
	} else {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}

	targetUserName := r.FormValue("user")
	fCache.Del(userName, targetUserName)

	http.Redirect(w, r, "/", http.StatusFound)
}

func getSession(w http.ResponseWriter, r *http.Request) *sessions.Session {
	session, _ := store.Get(r, sessionName)

	return session
}

func pathURIEscape(s string) string {
	//return (&url.URL{Path: s}).String()
	return s
}

func badRequest(w http.ResponseWriter) {
	code := http.StatusBadRequest
	http.Error(w, http.StatusText(code), code)
}

func userHandler(w http.ResponseWriter, r *http.Request) {
	var name string
	session := getSession(w, r)
	sessionUID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(sessionUID.(int))
	} else {
		name = ""
	}

	user := mux.Vars(r)["user"]
	mypage := user == name

	userID := getuserID(user)
	if userID == 0 {
		http.NotFound(w, r)
		return
	}

	isFriend := false
	if name != "" {
		result, err := loadFriends(name)
		if err != nil {
			badRequest(w)
			return
		}

		for _, x := range result {
			if x == user {
				isFriend = true
				break
			}
		}
	}

	until := r.URL.Query().Get("until")
	var rows *sql.Rows
	var err error
	if until == "" {
		rows, err = db.Query(`SELECT * FROM tweets WHERE user_id = ? ORDER BY id DESC LIMIT ?`, userID, perPage)
	} else {
		rows, err = db.Query(`SELECT * FROM tweets WHERE user_id = ? AND created_at < ? ORDER BY id DESC LIMIT ?`, userID, until, perPage)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		return
	}
	defer rows.Close()

	tweets := make([]*Tweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.ID, &t.UserID, &t.Text, &t.CreatedAt)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			return
		}
		t.HTML = htmlify(t.Text)
		t.Time = t.CreatedAt.Format("2006-01-02 15:04:05")
		t.UserName = user
		tweets = append(tweets, &t)

		if len(tweets) == perPage {
			break
		}
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*Tweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "user", struct {
		Name     string
		User     string
		Tweets   []*Tweet
		IsFriend bool
		Mypage   bool
	}{
		name, user, tweets, isFriend, mypage,
	})
}

func searchHandler(w http.ResponseWriter, r *http.Request) {
	var name string
	session := getSession(w, r)
	userID, ok := session.Values["user_id"]
	if ok {
		name = getUserName(userID.(int))
	} else {
		name = ""
	}

	query := r.URL.Query().Get("q")
	if mux.Vars(r)["tag"] != "" {
		query = "#" + mux.Vars(r)["tag"]
	}

	until := r.URL.Query().Get("until")
	var rows *sql.Rows
	var err error
	if until == "" {
		rows, err = db.Query(`SELECT tweets.* FROM tweets FORCE INDEX (PRIMARY) WHERE text LIKE ? ORDER BY id DESC LIMIT ?`, "%"+query+"%", perPage)
	} else {
		rows, err = db.Query(`SELECT tweets.* FROM tweets FORCE INDEX (PRIMARY) WHERE text LIKE ? AND created_at < ? ORDER BY id DESC LIMIT ?`, "%"+query+"%", until, perPage)
	}
	if err != nil {
		if err == sql.ErrNoRows {
			http.NotFound(w, r)
			return
		}
		badRequest(w)
		return
	}
	defer rows.Close()

	tweets := make([]*Tweet, 0)
	for rows.Next() {
		t := Tweet{}
		err := rows.Scan(&t.ID, &t.UserID, &t.Text, &t.CreatedAt)
		if err != nil && err != sql.ErrNoRows {
			badRequest(w)
			return
		}
		t.HTML = htmlify(t.Text)
		t.Time = t.CreatedAt.Format("2006-01-02 15:04:05")
		if strings.Index(t.HTML, query) != -1 {
			tweets = append(tweets, &t)
		}

		if len(tweets) == perPage {
			break
		}
	}
	err = fillUserNames(tweets)
	if err != nil {
		badRequest(w)
		return
	}

	add := r.URL.Query().Get("append")
	if add != "" {
		re.HTML(w, http.StatusOK, "_tweets", struct {
			Tweets []*Tweet
		}{
			tweets,
		})
		return
	}

	re.HTML(w, http.StatusOK, "search", struct {
		Name   string
		Tweets []*Tweet
		Query  string
	}{
		name, tweets, query,
	})
}

func js(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript")
	w.Write(fileRead("./public/js/script.js"))
}

func css(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css")
	w.Write(fileRead("./public/css/style.css"))
}

func fileRead(fp string) []byte {
	fs, err := os.Open(fp)

	if err != nil {
		return nil
	}

	defer fs.Close()

	l, err := fs.Stat()

	if err != nil {
		return nil
	}

	buf := make([]byte, l.Size())

	_, err = fs.Read(buf)

	if err != nil {
		return nil
	}

	return buf
}

func main() {
	host := os.Getenv("ISUWITTER_DB_HOST")
	if host == "" {
		host = "localhost"
	}
	port := os.Getenv("ISUWITTER_DB_PORT")
	if port == "" {
		port = "3306"
	}
	user := os.Getenv("ISUWITTER_DB_USER")
	if user == "" {
		user = "root"
	}
	password := os.Getenv("ISUWITTER_DB_PASSWORD")
	dbname := os.Getenv("ISUWITTER_DB_NAME")
	if dbname == "" {
		dbname = "isuwitter"
	}

	isutomoDBName := os.Getenv("ISUTOMO_DB_NAME")
	if isutomoDBName == "" {
		isutomoDBName = "isutomo"
	}

	var err error
	db, err = sql.Open("mysql:trace", fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&loc=Local&parseTime=true",
		user, password, host, port, dbname,
	))
	if err != nil {
		log.Fatalf("Failed to connect to DB: %s.", err.Error())
	}

	dbTomo, err = sql.Open("mysql:trace", fmt.Sprintf(
		"%s:%s@tcp(%s:%s)/%s?charset=utf8mb4&loc=Local&parseTime=true",
		user, password, host, port, isutomoDBName,
	))
	if err != nil {
		log.Fatalf("Failed to connect to DB: %s.", err.Error())
	}

	store = sessions.NewFilesystemStore("", []byte(sessionSecret))

	re = render.New(render.Options{
		Directory: "views",
		Funcs: []template.FuncMap{
			{
				"raw": func(text string) template.HTML {
					return template.HTML(text)
				},
				"add": func(a, b int) int { return a + b },
			},
		},
	})

	r := mux.NewRouter()
	r.HandleFunc("/initialize", initializeHandler).Methods("GET")

	l := r.PathPrefix("/login").Subrouter()
	l.Methods("POST").HandlerFunc(loginHandler)
	r.HandleFunc("/logout", logoutHandler)

	r.PathPrefix("/css/style.css").HandlerFunc(css)
	r.PathPrefix("/js/script.js").HandlerFunc(js)

	s := r.PathPrefix("/search").Subrouter()
	s.Methods("GET").HandlerFunc(searchHandler)
	t := r.PathPrefix("/hashtag/{tag}").Subrouter()
	t.Methods("GET").HandlerFunc(searchHandler)

	n := r.PathPrefix("/unfollow").Subrouter()
	n.Methods("POST").HandlerFunc(unfollowHandler)
	f := r.PathPrefix("/follow").Subrouter()
	f.Methods("POST").HandlerFunc(followHandler)

	u := r.PathPrefix("/{user}").Subrouter()
	u.Methods("GET").HandlerFunc(userHandler)

	i := r.PathPrefix("/").Subrouter()
	i.Methods("GET").HandlerFunc(topHandler)
	i.Methods("POST").HandlerFunc(tweetPostHandler)

	sigchan := make(chan os.Signal)
	signal.Notify(sigchan, syscall.SIGTERM)
	signal.Notify(sigchan, syscall.SIGINT)

	var li net.Listener
	var herr error
	hsock := "/dev/shm/server.sock"
	if hport == 0 {
		ferr := os.Remove(hsock)
		if ferr != nil {
			if !os.IsNotExist(ferr) {
				panic(ferr)
			}
		}
		li, herr = net.Listen("unix", hsock)
		cerr := os.Chmod(hsock, 0666)
		if cerr != nil {
			panic(cerr)
		}
	} else {
		li, herr = net.ListenTCP("tcp", &net.TCPAddr{Port: hport})
	}
	if herr != nil {
		panic(herr)
	}
	go func() {
		// func Serve(l net.Listener, handler Handler) error
		log.Println(http.Serve(li, r))
	}()

	<-sigchan
}
