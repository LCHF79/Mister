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
}

//Response struct
type Response struct {
	Temperature []Sensor
	Relays      []Relay
}

var temps []Sensor
var relays []Relay

// ScheduleCheckTemps func
func ScheduleCheckTemps() {
	ticker := time.NewTicker(60 * time.Second)
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

// CheckTemps func
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
	if SwitchRelay(s, r.FormValue("state")) == true {
		TestTemplate(w, r)
	}
}

// SwitchRelay func
func SwitchRelay(pin uint8, state string) bool {
	err := rpio.Open()
	if err != nil {
		fmt.Printf(err.Error())
		os.Exit(1)
	}

	defer rpio.Close()

	var rt time.Time

	rpio.Pin(pin).Output()
	if state == "on" {
		rt = time.Now().Local().Add(time.Minute * 3)
		rpio.Pin(pin).Low()
	} else {
		rpio.Pin(pin).High()
		rt = time.Now().Local()
	}

	for i, p := range relays {
		if p.Pin == pin {
			var r []Relay
			r = append(r, relays[:i]...)
			r = append(r, Relay{p.ID, p.Description, p.Pin, uint8(rpio.Pin(pin).Read()), rt})
			if len(relays) > i {
				r = append(r, relays[i+1:]...)
			}
			relays = r
			fmt.Println(relays)
			return true
		}
	}
	return false
}

// DutyCycle func
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
	srv := &http.Server{
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		Addr:         ":80",
		Handler:      mux,
	}
	log.Fatal(srv.ListenAndServe())

}
