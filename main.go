package main

import (
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

	"github.com/fvbock/endless"
	"github.com/mediocregopher/radix.v2/pool"
	"github.com/mediocregopher/radix.v2/redis"
	rpio "github.com/stianeikeland/go-rpio"
	"github.com/yryz/ds18b20"
)

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

	fmt.Println("Before receive rr")
	r := read()

	for i, p := range r {
		if p.Pin == pin {
			rpio.Pin(pin).Output()
			if state == "on" {
				if rpio.Pin(pin).Read() == 0 {
					dt = p.DutyTime
					rt = time.Now().Local().Add(time.Minute * 3)
				} else {
					rt = time.Now().Local().Add(time.Minute * 3)
					dt = time.Now().Local()
					rpio.Pin(pin).Low()
				}
			} else {
				rpio.Pin(pin).High()
				rt = time.Now().Local()
				time.Now().Local()
			}
			var rel []Relay
			rel = r[:i]
			rel = append(rel, Relay{p.ID, p.Description, p.Pin, uint8(rpio.Pin(pin).Read()), rt, dt})
			if len(r) > i {
				rel = append(r, rel[i+1:]...)
			}
			write(rel)
			fmt.Println(relays)
		}
	}
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

// TestTemplate func
func TestTemplate(w http.ResponseWriter, r *http.Request) {
	fmap := template.FuncMap{
		"GetState":      GetState,
		"ToggleState":   ToggleState,
		"GetStateClass": GetStateClass,
	}
	tmpl := template.Must(template.New("layout.html").Funcs(fmap).ParseFiles("layout.html"))

	res := Response{
		Temperature: temps,
		Relays:      relays,
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
	resp := conn.Cmd("HMSET", "album:1", "title", "Electric Ladyland", "artist", "Jimi Hendrix", "price", 4.95, "likes", 8)
	// Check the Err field of the *Resp object for any errors.
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}

}

// main function to boot up everything
func main() {
	conn, err := redis.Dial("tcp", "localhost:6379")
	if err != nil {
		log.Fatal(err)
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
	http.HandleFunc("/like", addLike)

	logFile, err := os.OpenFile("log.txt", os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
	if err != nil {
		panic(err)
	}
	mw := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(mw)

	resp := conn.Cmd("HMSET", "album:1", "title", "Electric Ladyland", "artist", "Jimi Hendrix", "price", 4.95, "likes", 8)
	// Check the Err field of the *Resp object for any errors.
	if resp.Err != nil {
		log.Fatal(resp.Err)
	}

	fmt.Println("Electric Ladyland added!!")
	err = endless.ListenAndServe(":80", mux)
	if err != nil {
		log.Println(err)
	}

}

/*
func ToggleRPIO() {
	for {
		quit := make(chan struct{})
		r := make(chan Relay)
		go func() {
			for {
				select {
				case <-r:
					if r.State == 1 {
						rpio.Pin(r.Pin).High()
					} else {
						rpio.Pin(r.Pin).Low()
					}

				case <-quit:
					ticker.Stop()
					return
				}
			}
		}()
	}
}
*/

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
