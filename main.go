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
	"sync"
	"time"

	"github.com/fvbock/endless"
	rpio "github.com/stianeikeland/go-rpio"
	"github.com/yryz/ds18b20"
)

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
	err := rpio.Open()
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	defer rpio.Close()

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
	err := rpio.Open()
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	defer rpio.Close()

	var dt time.Time
	r := read()
	var rel []Relay

	for _, p := range r {
		rpio.Pin(p.Pin).Output()
		if p.RunTill.Sub(time.Now()) > 0 {
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
	err := rpio.Open()
	if err != nil {
		log.Println(err)
		os.Exit(1)
	}
	defer rpio.Close()

	var r []Relay
	r = append(r, Relay{
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
	write(r)
}

// main function to boot up everything
func main() {
	ScheduleCheckTemps()
	InitRelays()
	mux := http.NewServeMux()
	mux.HandleFunc("/temp", GetTemps)
	mux.HandleFunc("/switch", HandleSwitch)
	mux.HandleFunc("/", TestTemplate)

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
