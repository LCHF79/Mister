package main

import (
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

type Config2 struct {
	Database: {
		MssqlServer string `json:"server"`
		User        string `json:"user"`
		Pass        string `json:"password"`
		Port        string `json:"port"`
		Database    string `json:"database"`
	} `json:"database"`
	Username: string `json:"username"`
	Password: string `json:"password"`
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

//CoolroomTemps struct
type CoolroomTemps struct {
	Tag   string `json:"tag"`
	Value string `json:"value"`
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
var msg chan Relay

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
			msg <- Relay{Pin: pin, State: 0}
			//rpio.Pin(pin).Low()
		}
	} else {
		st = 1
		msg <- Relay{Pin: pin, State: 1}
		//pio.Pin(pin).High()
		rt = time.Now().Local()
		dt = time.Now().Local()
	}
	fmt.Println(time.Now().Unix())
	go LogSwitch(reply["Description"], state, time.Now())

	rel := Relay{
		Description: reply["Description"],
		Pin:         pin,
		State:       st,
		RunTill:     rt,
		DutyTime:    dt,
	}
	rWrite(rel)
}

// DutyCycle func
func DutyCycle() {
	var dt time.Time
	r, _ := rRead()

	for _, p := range r {
		rpio.Pin(p.Pin).Output()
		if p.RunTill.Sub(time.Now()) < 0 {
			if uint8(rpio.Pin(p.Pin).Read()) == 0 {
				//rpio.Pin(p.Pin).High()
				msg <- Relay{Pin: p.Pin, State: 1}
				go LogSwitch(p.Description, "off", time.Now())
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
		rel := Relay{p.ID, p.Description, p.Pin, uint8(rpio.Pin(p.Pin).Read()), p.RunTill, dt}
		rWrite(rel)
	}
}

// Switch func
func switcher() {
	for {
		select {
		case rel := <-msg:
			if rel.State == 1 {
				rpio.Pin(rel.Pin).High()
			} else {
				rpio.Pin(rel.Pin).Low()
			}
			time.Sleep(time.Millisecond * 800)
			fmt.Printf("Message Received: %d %d\n", rel.Pin, rel.State)
		}
	}
}

// LogSwitch func
func LogSwitch(sy string, st string, t time.Time) {
	res, err := mssqldb.Exec(`INSERT INTO MistingLogs VALUES (?, ?, ?)`, sy, st, t)
	if err != nil {
		fmt.Println("Exec err:", err.Error())
	} else {
		rows, _ := res.RowsAffected()
		if rows == 1 {
			fmt.Println("1 row inserted")
		}
	}
}

// TestTemplate func
func TestTemplate(w http.ResponseWriter, r *http.Request) {
	userName := getUserName(r)
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

func coolroomloghandler(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("%v\n", r.URL.String())
	conn, err := db.Get()
	if err != nil {
		panic(err)
	}
	defer db.Put(conn)

	r.ParseForm()

	fmt.Printf("Form body: %s", r.Body)

	for key, values := range r.Form { // range over map
		for _, value := range values { // range over []string
			fmt.Printf("key=%s, value=%s\n", key, values)
			resp := conn.Cmd("HMSET", "cr:"+key, "Value", value)
			if resp.Err != nil {
				log.Fatal(resp.Err)
			}
		}
	}

	fmt.Fprintf(w, "Done!")
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
}

// main function to boot up everything
func main() {
	msg = make(chan Relay, 10)
	go switcher()
	config, err := LoadConfiguration("sqlcon.json")
	config2, err := LoadConfiguration2("config.json")
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
	mux.HandleFunc("/auth", AuthFunc)
	mux.HandleFunc("/login", loginHandler)
	mux.HandleFunc("/coolroomlogs", coolroomloghandler)

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

	configFile, err := os.Open(file)
	defer configFile.Close()
	if err != nil {
		fmt.Println(err.Error())
	}
	jsonParser := json.NewDecoder(configFile)
	err = jsonParser.Decode(&config)
	return config, err
}

//LoadConfiguration2 is a func
func LoadConfiguration2(file string) (Config2, error) {
	var config Config2

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
