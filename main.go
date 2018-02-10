package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	_ "github.com/denisenkom/go-mssqldb"
	"github.com/fvbock/endless"
	"github.com/gorilla/securecookie"
	"github.com/mediocregopher/radix.v2/pool"
	"github.com/mediocregopher/radix.v2/redis"
	rpio "github.com/stianeikeland/go-rpio"
	"github.com/yryz/ds18b20"
)

var mssqldb *sql.DB

//Config is a struct dumb ass
type Config struct {
	MssqlServer string `json:"server"`
	User        string `json:"user"`
	Pass        string `json:"password"`
	Port        string `json:"port"`
	Database    string `json:"database"`
}

var cookieHandler = securecookie.New(securecookie.GenerateRandomKey(64), securecookie.GenerateRandomKey(32))
var db *pool.Pool
var errNoAlbum = errors.New("models: no album found")

//Album struct
type Album struct {
	Title  string
	Artist string
	Price  float64
	Likes  int
}

//Sensor struct
type Sensor struct {
	ID    string  `json:"id,omitempty"`
	Value float64 `json:"value,omitempty"`
}

//Relay struct
type Relay struct {
	ID          int       `json:"id,omitempty"`
	Description string    `json:"description,omitempty"`
	Pin         uint8     `json:"pin,omitempty"`
	State       uint8     `json:"value,omitempty"`
	RunTill     time.Time `json:"runtill,omitempty"`
	DutyTime    time.Time `json:"dutytime,omitempty"`
}

//Response struct
type Response struct {
	Temperature []Sensor
	Relays      []Relay
}

var temps []Sensor
var relays []Relay
var lock = sync.RWMutex{}
var systems [3]string

func init() {
	var err error
	// Establish a pool of 10 connections to the Redis server listening on
	// port 6379 of the local machine.
	db, err = pool.New("tcp", "localhost:6379", 10)
	if err != nil {
		log.Panic(err)
	}
}

// ScheduleCheckTemps func
func ScheduleCheckTemps() {
	fmt.Println("Schedule func started")
	ticker := time.NewTicker(60 * time.Second)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				go CheckTemps()
				go DutyCycle()
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
}

// CheckTemps func
func CheckTemps() {
	temps = nil
	sensors, err := ds18b20.Sensors()
	if err != nil {
		log.Println(err)
	}

	for _, sensor := range sensors {
		t, err := ds18b20.Temperature(sensor)
		if err == nil {
			fmt.Printf("sensor: %s temperature: %.2f°C\n", sensor, t)
			temps = append(temps, Sensor{ID: sensor, Value: t})
		} else {
			log.Println(err)
		}
	}
}

// GetTemps func
func GetTemps(w http.ResponseWriter, r *http.Request) {
	res := Response{
		Temperature: temps,
		Relays:      relays,
	}

	json.NewEncoder(w).Encode(res)
}

// HandleSwitch func
func HandleSwitch(w http.ResponseWriter, r *http.Request) {
	userName := getUserName(r)
	if userName == "" {
		http.Redirect(w, r, "/aut", 302)
		return
	}
	fmt.Printf("%v\n", r.URL.String())
	p := r.FormValue("pin")
	q, _ := strconv.ParseInt(p, 10, 8)
	s := uint8(q)
	SwitchRelay(s, r.FormValue("state"))
	TestTemplate(w, r)
}

// SwitchRelay func
func SwitchRelay(pin uint8, state string) {
	var rt time.Time
	var dt time.Time
	var st uint8

	conn, err := db.Get()
	if err != nil {
		panic(err)
	}
	defer db.Put(conn)

	reply, err := conn.Cmd("HGETALL", "Pin:"+strconv.Itoa(int(pin))).Map()
	if err != nil {
		panic(err)
	} else if len(reply) == 0 {
		panic(errNoAlbum)
	}
	rpio.Pin(pin).Output()
	if state == "on" {
		st = 0
		if rpio.Pin(pin).Read() == 0 {
			if reply["DutyTime"] != "" {
				t, err := strconv.ParseInt(reply["DutyTime"], 0, 64)
				if err != nil {
					panic(err)
				}
				dt = time.Unix(t, 0)
			} else {
				dt = time.Now().Local()
			}
			rt = time.Now().Local().Add(time.Minute * 3)
		} else {
			rt = time.Now().Local().Add(time.Minute * 3)
			dt = time.Now().Local()
			rpio.Pin(pin).Low()
		}
	} else {
		st = 1
		rpio.Pin(pin).High()
		rt = time.Now().Local()
		dt = time.Now().Local()
	}

	rows, err := mssqldb.Query("INSERT INTO MistingLogs (?, ?, ?)", reply["Description"], GetState(st), time.Now().Local())
	if err != nil {
		fmt.Println("Cannot query: ", err.Error())
		return
	}
	defer rows.Close()
	for rows.Next() {
		var val []interface{}
		err = rows.Scan(val...)
		if err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Println(val)
	}

	rel := Relay{
		Description: reply["Description"],
		Pin:         pin,
		State:       st,
		RunTill:     rt,
		DutyTime:    dt,
	}
	rWrite(rel)
	fmt.Println(rRead())
}

// DutyCycle func
func DutyCycle() {
	var dt time.Time
	r := read()
	var rel []Relay

	for _, p := range r {
		rpio.Pin(p.Pin).Output()
		if p.RunTill.Sub(time.Now()) < 0 {
			if uint8(rpio.Pin(p.Pin).Read()) == 0 {
				rpio.Pin(p.Pin).High()
			}
			/*
				if p.State == 1 {
					if p.DutyTime.Sub(time.Now().Add(time.Second*100)) < 0 {
						rpio.Pin(p.Pin).Low()
						dt = time.Now().Local()
					} else {
						dt = p.DutyTime
					}
				} else {
					rpio.Pin(p.Pin).High()
					dt = time.Now().Local()
				}
			*/
		}
		dt = p.DutyTime
		rel = append(rel, Relay{p.ID, p.Description, p.Pin, uint8(rpio.Pin(p.Pin).Read()), p.RunTill, dt})
		write(rel)
	}
}

// Switch func
func Switch() {
	for {

	}
}

// TestTemplate func
func TestTemplate(w http.ResponseWriter, r *http.Request) {
	userName := getUserName(r)
	fmt.Printf("username %s\n", userName)
	if userName == "" {
		http.Redirect(w, r, "/auth", 302)
		return
	}
	fmap := template.FuncMap{
		"GetState":      GetState,
		"ToggleState":   ToggleState,
		"GetStateClass": GetStateClass,
	}
	tmpl := template.Must(template.New("layout.html").Funcs(fmap).ParseFiles("layout.html"))
	rel, _ := rRead()
	res := Response{
		Temperature: temps,
		Relays:      rel,
	}
	tmpl.Execute(w, res)
}

// GetState func
func GetState(s uint8) string {
	if s == 1 {
		return "Off"
	}
	return "On"

}

// GetStateClass func
func GetStateClass(s uint8) string {
	if s == 1 {
		return "table-light"
	}
	return "table-primary"

}

// ToggleState func
func ToggleState(s uint8) string {
	if s == 1 {
		return "on"
	}
	return "off"

}

// InitRelays func
func InitRelays() {
	var r []Relay
	r = append(r, Relay{
		ID:          2,
		Description: "System A",
		Pin:         6,
		State:       1,
	},
		Relay{
			ID:          3,
			Description: "System B",
			Pin:         7,
			State:       1,
		},
		Relay{
			ID:          4,
			Description: "System C",
			Pin:         8,
			State:       1,
		},
	)
	write(r)
	conn, err := db.Get()
	if err != nil {
		panic(err)
	}
	defer db.Put(conn)

	resp := conn.Cmd("HMSET", "Pin:6", "Description", "System A", "Pin", 6, "State", 1)
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}
	resp = conn.Cmd("HMSET", "Pin:7", "Description", "System B", "Pin", 7, "State", 1)
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}
	resp = conn.Cmd("HMSET", "Pin:8", "Description", "System C", "Pin", 8, "State", 1)
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}
	systems[0] = "Pin:6"
	systems[1] = "Pin:7"
	systems[2] = "Pin:8"

	resp = conn.Cmd("HMSET", "album:1", "title", "Electric Ladyland", "artist", "Jimi Hendrix", "price", 4.95, "likes", 8)
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}
	resp = conn.Cmd("HMSET", "album:2", "title", "Back in Black", "artist", "AC/DC", "price", 5.95, "likes", 3)
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}
	resp = conn.Cmd("HMSET", "album:3", "title", "Rumours", "artist", "Fleetwood Mac", "price", 7.95, "likes", 7)
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}
	resp = conn.Cmd("HMSET", "album:4", "title", "Nevermind", "artist", "Nirvana", "price", 8.95, "likes", 11)
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}
	resp = conn.Cmd("ZADD", "likes", 8, 1, 3, 2, 12, 3, 8, 4)
	// Check the Err field of the *Resp object for any errors.
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}

}

// main function to boot up everything
func main() {
	config, err := LoadConfiguration("sqlcon.json")

	fmt.Println(config)
	connString := fmt.Sprintf("server=%s;user id=%s;password=%s;port=%s;database=%s;",
		config.MssqlServer, config.User, config.Pass, config.Port, config.Database)
	fmt.Println(connString)
	// Create connection pool
	mssqldb, err = sql.Open("mssql", connString)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Connected!\n")
	ctx := context.Background()

	err = mssqldb.PingContext(ctx)
	if err != nil {
		log.Fatal("Error pinging database: " + err.Error())
	}

	err = mssqldb.Ping()
	if err != nil {
		fmt.Println("Cannot connect: ", err.Error())
	}

	tsql := fmt.Sprintf("INSERT INTO MistingLogs (@system, @state, @time);")

	result, err := mssqldb.ExecContext(
		ctx,
		tsql,
		sql.Named("system", "system B"),
		sql.Named("state", "On"),
		sql.Named("time", time.Now().Local()))
	if err != nil {
		fmt.Println("Cannot query: ", err.Error())
		return
	}

	fmt.Println(result.LastInsertId())

	conn, err := redis.Dial("tcp", "localhost:6379")
	if err != nil {
		log.Fatal("Error creating connection pool:", err.Error())
	}

	defer conn.Close()
	err = rpio.Open()
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	defer rpio.Close()
	ScheduleCheckTemps()
	InitRelays()
	mux := http.NewServeMux()
	mux.HandleFunc("/temp", GetTemps)
	mux.HandleFunc("/switch", HandleSwitch)
	mux.HandleFunc("/", TestTemplate)
	mux.HandleFunc("/album", showAlbum)
	mux.HandleFunc("/like", addLike)
	mux.HandleFunc("/popular", listPopular)
	mux.HandleFunc("/auth", AuthFunc)
	mux.HandleFunc("/login", loginHandler)

	logFile, err := os.OpenFile("log.txt", os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
	if err != nil {
		panic(err)
	}
	mw := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(mw)

	err = endless.ListenAndServe(":80", mux)
	if err != nil {
		log.Println(err)
	}

}

//LoadConfiguration is a func
func LoadConfiguration(file string) (Config, error) {
	var config Config

	fmt.Println(file)
	configFile, err := os.Open(file)
	defer configFile.Close()
	if err != nil {
		fmt.Println(err.Error())
	}
	fmt.Println(configFile)
	jsonParser := json.NewDecoder(configFile)
	err = jsonParser.Decode(&config)
	fmt.Println(config)
	return config, err
}

func read() []Relay {
	lock.RLock()
	defer lock.RUnlock()
	var r []Relay
	r = append(r, relays...)
	return r
}

func write(r []Relay) {
	lock.Lock()
	defer lock.Unlock()
	relays = r
}

func rRead() ([]Relay, error) {
	// Use the connection pool's Get() method to fetch a single Redis
	// connection from the pool.
	var r []Relay
	conn, err := db.Get()
	if err != nil {
		return nil, err
	}
	// Importantly, use defer and the connection pool's Put() method to ensure
	// that the connection is always put back in the pool before FindAlbum()
	// exits.
	defer db.Put(conn)

	// Fetch the details of a specific album. If no album is found with the
	// given id, the map[string]string returned by the Map() helper method
	// will be empty. So we can simply check whether it's length is zero and
	// return an ErrNoAlbum message if necessary.
	var pp int64
	var ss int64
	var rt int64
	var dt int64
	for _, s := range systems {
		fmt.Println(s)
		reply, err := conn.Cmd("HGETALL", s).Map()
		if err != nil {
			return nil, err
		} else if len(reply) == 0 {
			return nil, errNoAlbum
		}
		pp, _ = strconv.ParseInt(reply["Pin"], 0, 64)
		ss, _ = strconv.ParseInt(reply["State"], 0, 64)
		rt, _ = strconv.ParseInt(reply["RunTill"], 0, 64)
		dt, _ = strconv.ParseInt(reply["DutyTime"], 0, 64)
		r = append(r, Relay{
			Description: reply["Description"],
			Pin:         uint8(pp),
			State:       uint8(ss),
			RunTill:     time.Unix(rt, 0),
			DutyTime:    time.Unix(dt, 0),
		})
	}

	return r, nil
}

func rWrite(r Relay) {
	conn, err := db.Get()
	if err != nil {
		panic(err)
	}
	defer db.Put(conn)
	resp := conn.Cmd("HMSET", "Pin:"+strconv.Itoa(int(r.Pin)), "Description", r.Description, "Pin", r.Pin, "State", r.State, "RunTill", int64(r.RunTill.Unix()), "DutyTime", int64(r.DutyTime.Unix()))
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}
}

func readRW() []Relay {
	var r []Relay
	r = append(r, relays...)
	return r
}

func showAlbum(w http.ResponseWriter, r *http.Request) {
	// Unless the request is using the GET method, return a 405 'Method Not
	// Allowed' response.
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		http.Error(w, http.StatusText(405), 405)
		return
	}

	// Retrieve the id from the request URL query string. If there is no id
	// key in the query string then Get() will return an empty string. We
	// check for this, returning a 400 Bad Request response if it's missing.
	id := r.URL.Query().Get("id")
	if id == "" {
		http.Error(w, http.StatusText(400), 400)
		return
	}
	// Validate that the id is a valid integer by trying to convert it,
	// returning a 400 Bad Request response if the conversion fails.
	if _, err := strconv.Atoi(id); err != nil {
		http.Error(w, http.StatusText(400), 400)
		return
	}

	// Call the FindAlbum() function passing in the user-provided id. If
	// there's no matching album found, return a 404 Not Found response. In
	// the event of any other errors, return a 500 Internal Server Error
	// response.
	bk, err := findAlbum(id)
	if err == errNoAlbum {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, http.StatusText(500), 500)
		return
	}

	// Write the album details as plain text to the client.
	fmt.Fprintf(w, "%s by %s: £%.2f [%d likes] \n", bk.Title, bk.Artist, bk.Price, bk.Likes)
}
func findAlbum(id string) (*Album, error) {
	// Use the connection pool's Get() method to fetch a single Redis
	// connection from the pool.
	conn, err := db.Get()
	if err != nil {
		return nil, err
	}
	// Importantly, use defer and the connection pool's Put() method to ensure
	// that the connection is always put back in the pool before FindAlbum()
	// exits.
	defer db.Put(conn)

	// Fetch the details of a specific album. If no album is found with the
	// given id, the map[string]string returned by the Map() helper method
	// will be empty. So we can simply check whether it's length is zero and
	// return an ErrNoAlbum message if necessary.
	reply, err := conn.Cmd("HGETALL", "album:"+id).Map()
	if err != nil {
		return nil, err
	} else if len(reply) == 0 {
		return nil, errNoAlbum
	}

	return populateAlbum(reply)
}

func populateAlbum(reply map[string]string) (*Album, error) {
	var err error
	ab := new(Album)
	ab.Title = reply["title"]
	ab.Artist = reply["artist"]
	ab.Price, err = strconv.ParseFloat(reply["price"], 64)
	if err != nil {
		return nil, err
	}
	ab.Likes, err = strconv.Atoi(reply["likes"])
	if err != nil {
		return nil, err
	}
	return ab, nil
}

func incrementLikes(id string) error {
	conn, err := db.Get()
	if err != nil {
		return err
	}
	defer db.Put(conn)

	// Before we do anything else, check that an album with the given id
	// exists. The EXISTS command returns 1 if a specific key exists
	// in the database, and 0 if it doesn't.
	exists, err := conn.Cmd("EXISTS", "album:"+id).Int()
	if err != nil {
		return err
	} else if exists == 0 {
		return errNoAlbum
	}

	// Use the MULTI command to inform Redis that we are starting a new
	// transaction.
	err = conn.Cmd("MULTI").Err
	if err != nil {
		return err
	}

	// Increment the number of likes in the album hash by 1. Because it
	// follows a MULTI command, this HINCRBY command is NOT executed but
	// it is QUEUED as part of the transaction. We still need to check
	// the reply's Err field at this point in case there was a problem
	// queueing the command.
	err = conn.Cmd("HINCRBY", "album:"+id, "likes", 1).Err
	if err != nil {
		return err
	}
	// And we do the same with the increment on our sorted set.
	err = conn.Cmd("ZINCRBY", "likes", 1, id).Err
	if err != nil {
		return err
	}

	// Execute both commands in our transaction together as an atomic group.
	// EXEC returns the replies from both commands as an array reply but,
	// because we're not interested in either reply in this example, it
	// suffices to simply check the reply's Err field for any errors.
	err = conn.Cmd("EXEC").Err
	if err != nil {
		return err
	}
	return nil
}

func addLike(w http.ResponseWriter, r *http.Request) {
	// Unless the request is using the POST method, return a 405 'Method Not
	// Allowed' response.
	if r.Method != "POST" {
		w.Header().Set("Allow", "POST")
		http.Error(w, http.StatusText(405), 405)
		return
	}

	// Retreive the id from the POST request body. If there is no parameter
	// named "id" in the request body then PostFormValue() will return an
	// empty string. We check for this, returning a 400 Bad Request response
	// if it's missing.
	id := r.PostFormValue("id")
	if id == "" {
		http.Error(w, http.StatusText(400), 400)
		return
	}
	// Validate that the id is a valid integer by trying to convert it,
	// returning a 400 Bad Request response if the conversion fails.
	if _, err := strconv.Atoi(id); err != nil {
		http.Error(w, http.StatusText(400), 400)
		return
	}

	// Call the IncrementLikes() function passing in the user-provided id. If
	// there's no album found with that id, return a 404 Not Found response.
	// In the event of any other errors, return a 500 Internal Server Error
	// response.
	err := incrementLikes(id)
	if err == errNoAlbum {
		http.NotFound(w, r)
		return
	} else if err != nil {
		http.Error(w, http.StatusText(500), 500)
		return
	}

	// Redirect the client to the GET /ablum route, so they can see the
	// impact their like has had.
	http.Redirect(w, r, "/album?id="+id, 303)
}

func findTopThree() ([]*Album, error) {
	conn, err := db.Get()
	if err != nil {
		return nil, err
	}
	defer db.Put(conn)

	// Begin an infinite loop.
	for {
		// Instruct Redis to watch the likes sorted set for any changes.
		err = conn.Cmd("WATCH", "likes").Err
		if err != nil {
			return nil, err
		}

		// Use the ZREVRANGE command to fetch the album ids with the highest
		// score (i.e. most likes) from our 'likes' sorted set. The ZREVRANGE
		// start and stop values are zero-based indexes, so we use 0 and 2
		// respectively to limit the reply to the top three. Because ZREVRANGE
		// returns an array response, we use the List() helper function to
		// convert the reply into a []string.
		reply, err := conn.Cmd("ZREVRANGE", "likes", 0, 2).List()
		if err != nil {
			return nil, err
		}

		// Use the MULTI command to inform Redis that we are starting a new
		// transaction.
		err = conn.Cmd("MULTI").Err
		if err != nil {
			return nil, err
		}

		// Loop through the ids returned by ZREVRANGE, queuing HGETALL
		// commands to fetch the individual album details.
		for _, id := range reply {
			err := conn.Cmd("HGETALL", "album:"+id).Err
			if err != nil {
				return nil, err
			}
		}

		// Execute the transaction. Importantly, use the Resp.IsType() method
		// to check whether the reply from EXEC was nil or not. If it is nil
		// it means that another client changed the WATCHed likes sorted set,
		// so we use the continue command to re-run the loop.
		ereply := conn.Cmd("EXEC")
		if ereply.Err != nil {
			return nil, err
		} else if ereply.IsType(redis.Nil) {
			continue
		}

		// Otherwise, use the Array() helper function to convert the
		// transaction reply to an array of Resp objects ([]*Resp).
		areply, err := ereply.Array()
		if err != nil {
			return nil, err
		}

		// Create a new slice to store the album details.
		abs := make([]*Album, 3)

		// Iterate through the array of Resp objects, using the Map() helper
		// to convert the individual reply into a map[string]string, and then
		// the populateAlbum function to create a new Album object
		// from the map. Finally store them in order in the abs slice.
		for i, reply := range areply {
			mreply, err := reply.Map()
			if err != nil {
				return nil, err
			}
			ab, err := populateAlbum(mreply)
			if err != nil {
				return nil, err
			}
			abs[i] = ab
		}

		return abs, nil
	}
}

func listPopular(w http.ResponseWriter, r *http.Request) {
	// Unless the request is using the GET method, return a 405 'Method Not
	// Allowed' response.
	if r.Method != "GET" {
		w.Header().Set("Allow", "GET")
		http.Error(w, http.StatusText(405), 405)
		return
	}

	// Call the FindTopThree() function, returning a return a 500 Internal
	// Server Error response if there's any error.
	abs, err := findTopThree()
	if err != nil {
		http.Error(w, http.StatusText(500), 500)
		return
	}

	// Loop through the 3 albums, writing the details as a plain text list
	// to the client.
	for i, ab := range abs {
		fmt.Fprintf(w, "%d) %s by %s: £%.2f [%d likes] \n", i+1, ab.Title, ab.Artist, ab.Price, ab.Likes)
	}
}

func getUserName(request *http.Request) (userName string) {
	if cookie, err := request.Cookie("session"); err == nil {
		cookieValue := make(map[string]string)
		if err = cookieHandler.Decode("session", cookie.Value, &cookieValue); err == nil {
			userName = cookieValue["name"]
		}
	}
	return userName
}

func setSession(userName string, response http.ResponseWriter) {
	value := map[string]string{
		"name": userName,
	}
	if encoded, err := cookieHandler.Encode("session", value); err == nil {
		cookie := &http.Cookie{
			Name:  "session",
			Value: encoded,
			Path:  "/",
		}
		http.SetCookie(response, cookie)
	}
}

func clearSession(response http.ResponseWriter) {
	cookie := &http.Cookie{
		Name:   "session",
		Value:  "",
		Path:   "/",
		MaxAge: -1,
	}
	http.SetCookie(response, cookie)
}

// login handler

func loginHandler(response http.ResponseWriter, request *http.Request) {
	name := request.FormValue("name")
	pass := request.FormValue("password")
	fmt.Printf("name: %s\n", name)
	fmt.Printf("password: %s\n", pass)
	redirectTarget := "/auth"
	if name != "" && pass != "" {
		if name == "costas" && pass == "4BeachSt" {
			setSession(name, response)
			redirectTarget = "/"
		}

	}
	http.Redirect(response, request, redirectTarget, 302)
}

// logout handler

func logoutHandler(response http.ResponseWriter, request *http.Request) {
	clearSession(response)
	http.Redirect(response, request, "/", 302)
}

// index page

const indexPage = `
<h1>Login</h1>
<form method="post" action="/login">
    <label for="name">User name</label>
    <input type="text" id="name" name="name">
    <label for="password">Password</label>
    <input type="password" id="password" name="password">
    <button type="submit">Login</button>
</form>
`

// AuthFunc func
func AuthFunc(response http.ResponseWriter, request *http.Request) {
	fmt.Fprintf(response, indexPage)
}

// internal page

const internalPage = `
<h1>Internal</h1>
<hr>
<small>User: %s</small>
<form method="post" action="/logout">
    <button type="submit">Logout</button>
</form>
`

func internalPageHandler(response http.ResponseWriter, request *http.Request) {
	userName := getUserName(request)
	if userName != "" {
		fmt.Fprintf(response, internalPage, userName)
	} else {
		http.Redirect(response, request, "/auth", 302)
	}
}
