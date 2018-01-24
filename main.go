package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	"github.com/gorilla/mux"
	rpio "github.com/stianeikeland/go-rpio"
	"github.com/yryz/ds18b20"
)

// The person Type (more like an object njh)
type Person struct {
	ID        string   `json:"id,omitempty"`
	Firstname string   `json:"firstname,omitempty"`
	Lastname  string   `json:"lastname,omitempty"`
	Address   *Address `json:"address,omitempty"`
}
type Address struct {
	City  string `json:"city,omitempty"`
	State string `json:"state,omitempty"`
}

type Sensor struct {
	ID    string  `json:"id,omitempty"`
	Value float64 `json:"value,omitempty"`
}

type Relay struct {
	ID          int       `json:"id,omitempty"`
	Description string    `json:"description,omitempty"`
	Pin         uint8     `json:"pin,omitempty"`
	State       uint8     `json:"value,omitempty"`
	RunTill     time.Time `json:"runtill,omitempty"`
}

type Response struct {
	Temperature []Sensor
	Relays      []Relay
}

type Todo struct {
	Title string
	Done  bool
}

type TodoPageData struct {
	PageTitle string
	Todos     []Todo
}

var people []Person
var temps []Sensor
var relays []Relay

// Display all from the people var
func GetPeople(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(people)
}

// Display a single data
func GetPerson(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	for _, item := range people {
		if item.ID == params["id"] {
			json.NewEncoder(w).Encode(item)
			return
		}
	}
	json.NewEncoder(w).Encode(&Person{})
}

// create a new item
func CreatePerson(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	var person Person
	_ = json.NewDecoder(r.Body).Decode(&person)
	person.ID = params["id"]
	person.Firstname = params["firstname"]
	people = append(people, person)
	json.NewEncoder(w).Encode(people)
}

// Delete an item
func DeletePerson(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	for index, item := range people {
		if item.ID == params["id"] {
			people = append(people[:index], people[index+1:]...)
			break
		}
		json.NewEncoder(w).Encode(people)
	}
}

func ScheduleCheckTemps() {
	ticker := time.NewTicker(15 * time.Second)
	quit := make(chan struct{})
	go func() {
		for {
			select {
			case <-ticker.C:
				CheckTemps()
			case <-quit:
				ticker.Stop()
				return
			}
		}
	}()
}

func CheckTemps() {
	temps = nil
	sensors, err := ds18b20.Sensors()
	if err != nil {
		//panic(err)
	}

	for _, sensor := range sensors {
		t, err := ds18b20.Temperature(sensor)
		if err == nil {
			fmt.Printf("sensor: %s temperature: %.2f°C\n", sensor, t)
			temps = append(temps, Sensor{ID: sensor, Value: t})
		}
	}
}

func GetTemps(w http.ResponseWriter, r *http.Request) {
	res := Response{
		Temperature: temps,
		Relays:      relays,
	}

	json.NewEncoder(w).Encode(res)
}

func HandleSwitch(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Handler fired")
	fmt.Println(r.URL)
	p := r.FormValue("pin")
	q, _ := strconv.ParseInt(p, 10, 8)
	s := uint8(q)
	if SwitchRelay(s, r.FormValue("state")) == true {
		TestTemplate(w, r)
	}
}

func SwitchRelay(pin uint8, state string) bool {
	if err := rpio.Open(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer rpio.Close()
	rpio.Pin(pin).Output()
	fmt.Println(state)
	if state == "on" {
		fmt.Println("Turning on")
		rpio.Pin(pin).Low()
	} else {
		rpio.Pin(pin).High()
	}

	for i, p := range relays {
		if p.Pin == pin {
			r := relays[:i]
			r = append(r, Relay{p.ID, p.Description, p.Pin, uint8(rpio.Pin(pin).Read()), time.Now().Local().Add(time.Minute * 30)})
			if len(relays) > i {
				r = append(r, relays[i+1:]...)
			}
			fmt.Println(r)
			return true
		}
	}
	return false
}

func DutyCycle() {
	if err := rpio.Open(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer rpio.Close()

	for i, p := range relays {
		if p.RunTill.Sub(time.Now()) > 0 {
			rpio.Pin(p.Pin).Output()
			if p.State == 1 {
				rpio.Pin(p.Pin).Low()
			} else {
				rpio.Pin(p.Pin).High()
			}
			r := relays[:i]
			r = append(r, Relay{p.ID, p.Description, p.Pin, uint8(rpio.Pin(p.Pin).Read()), time.Now().Local().Add(time.Minute * 30)})
			if len(relays) > i {
				r = append(r, relays[i+1:]...)
			}
			fmt.Println(r)
		}
	}
}

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

func HandlePerson(w http.ResponseWriter, r *http.Request) {
	fmt.Println("Person route detected")
	if r.Method == "GET" {
		GetPerson(w, r)
	} else if r.Method == "POST" {
		CreatePerson(w, r)
	} else {
		DeletePerson(w, r)
	}
}

func GetState(s uint8) string {
	if s == 1 {
		return "Off"
	} else {
		return "On"
	}
}

func GetStateClass(s uint8) string {
	fmt.Println("Class func fired  class=\"table-primary\"")
	if s == 1 {
		return "table-light"
	} else {
		return "table-primary"
	}
}

func ToggleState(s uint8) string {
	if s == 1 {
		return "on"
	} else {
		return "off"
	}
}

func InitRelays() {
	if err := rpio.Open(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer rpio.Close()

	relays = append(relays, Relay{
		ID:          2,
		Description: "System A",
		Pin:         6,
		State:       uint8(rpio.Pin(6).Read()),
	},
		Relay{
			ID:          3,
			Description: "System B",
			Pin:         7,
			State:       uint8(rpio.Pin(7).Read()),
		},
		Relay{
			ID:          4,
			Description: "System C",
			Pin:         8,
			State:       uint8(rpio.Pin(8).Read()),
		},
	)

}

func BaseHandler(w http.ResponseWriter, r *http.Request) {
	fmt.Println(r.URL)
	json.NewEncoder(w).Encode(relays)
}

// main function to boot up everything
func main() {
	ScheduleCheckTemps()
	InitRelays()
	mux := http.NewServeMux()
	people = append(people, Person{ID: "1", Firstname: "John", Lastname: "Doe", Address: &Address{City: "City X", State: "State X"}})
	people = append(people, Person{ID: "2", Firstname: "Koko", Lastname: "Doe", Address: &Address{City: "City Z", State: "State Y"}})
	mux.HandleFunc("/test", TestTemplate)
	mux.HandleFunc("/people", GetPeople)
	mux.HandleFunc("/temp", GetTemps)
	mux.HandleFunc("/switch", HandleSwitch)
	mux.HandleFunc("/", BaseHandler)

	logFile, err := os.OpenFile("log.txt", os.O_CREATE|os.O_APPEND|os.O_RDWR, 0666)
	if err != nil {
		panic(err)
	}
	mw := io.MultiWriter(os.Stdout, logFile)
	log.SetOutput(mw)
	log.Fatal(http.ListenAndServe(":80", mux))

}
