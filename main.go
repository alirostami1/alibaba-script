package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/nikoksr/notify"
	"github.com/nikoksr/notify/service/telegram"
	"github.com/samber/lo"
	"golang.org/x/net/proxy"
)

type FlightService struct {
	Description        string                 `json:"description"`
	URL                string                 `json:"url"`
	Seats              *int                   `json:"seats"`
	MinPriceRial       *uint64                `json:"minPriceRial"`
	MaxPriceRial       *uint64                `json:"maxPriceRial"`
	DepartureHourStart *uint                  `json:"departureHourStart"`
	DepartureHourEnd   *uint                  `json:"departureHourEnd"`
	LastIteration      *Iteration[FlightInfo] `json:"lastIteration"`
}

type TrainService struct {
	Description         string                `json:"description"`
	URL                 string                `json:"url"`
	Seats               *int                  `json:"seats"`
	MinPriceRial        *uint64               `json:"minPriceRial"`
	MaxPriceRial        *uint64               `json:"maxPriceRial"`
	ShouldBeCompartment *bool                 `json:"shouldBeCompartment"`
	CompartmentCapacity *int                  `json:"compartmentCapacity"`
	DepartureHourStart  *uint                 `json:"departureHourStart"`
	DepartureHourEnd    *uint                 `json:"departureHourEnd"`
	LastIteration       *Iteration[TrainInfo] `json:"lastIteration"`
}

type Config struct {
	TelegramAPIKey       string          `json:"telegramApiKey"`
	TelegramChatID       int64           `json:"telegramChatId"`
	CheckIntervalSeconds int             `json:"checkIntervalSeconds"`
	TrainServices        []TrainService  `json:"trainServices"`
	FlightServices       []FlightService `json:"flightServices"`
	SocksUrl             *string         `json:"socksUrl"`
}

type FlightInfo struct {
	Origin          string `json:"origin"`
	Destination     string `json:"destination"`
	FlightNumber    string `json:"flightNumber"`
	LeaveDateTime   string `json:"leaveDateTime"`
	ArrivalDateTime string `json:"arrivalDateTime"`
	Aircraft        string `json:"aircraft"`
	Price           uint64 `json:"priceAdult"`
	ClassTypeName   string `json:"classTypeName"`
	Seat            int    `json:"seat"`
}

type TrainInfo struct {
	TrainNumber         int    `json:"trainNumber"`
	WagonName           string `json:"wagonName"`
	DepartureDateTime   string `json:"departureDateTime"`
	Seat                int    `json:"seat"`
	Cost                uint64 `json:"cost"`
	FullPrice           uint64 `json:"fullPrice"`
	IsCompartment       bool   `json:"isCompartment"`
	CompartmentCapacity int    `json:"compartmentCapacity"`
	CompanyName         string `json:"companyName"`
}

type FlightDeparture struct {
	Departing []FlightInfo `json:"departing"`
}

type TrainDeparture struct {
	Departing []TrainInfo `json:"departing"`
}

type FlightAPIResponse struct {
	Result FlightDeparture `json:"result"`
}

type TrainAPIResponse struct {
	Result TrainDeparture `json:"result"`
}

type Iteration[T comparable] struct {
	mu         *sync.Mutex
	Counter    uint64
	Collection []T
}

func (i *Iteration[T]) Replace(newCollection []T) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.Collection = newCollection
}

func (i *Iteration[T]) Get() []T {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.Collection
}

func (i *Iteration[T]) Diff(collection []T) []T {
	i.mu.Lock()
	defer i.mu.Unlock()
	_, diff := lo.Difference(i.Collection, collection)
	return diff
}

func main() {
	configFile, err := os.ReadFile("/etc/alibaba/config.json")
	if err != nil {
		log.Fatalf("reading config file: %+v", err)
	}

	var config Config
	err = json.Unmarshal(configFile, &config)
	if err != nil {
		log.Fatalf("unmarshaling config file: %+v", err)
	}

	for i := range config.TrainServices {
		config.TrainServices[i].LastIteration = &Iteration[TrainInfo]{
			mu:         &sync.Mutex{},
			Counter:    0,
			Collection: make([]TrainInfo, 0),
		}
	}
	for i := range config.FlightServices {
		config.FlightServices[i].LastIteration = &Iteration[FlightInfo]{
			mu:         &sync.Mutex{},
			Counter:    0,
			Collection: make([]FlightInfo, 0),
		}
	}

	telegramService, err := telegram.New(config.TelegramAPIKey)
	if err != nil {
		log.Fatalf("error creating telegram service: %+v", err)
	}
	telegramService.AddReceivers(config.TelegramChatID)
	notify.UseServices(telegramService)

	totalServices := len(config.FlightServices) + len(config.TrainServices)
	interval := float64(config.CheckIntervalSeconds) / float64(totalServices)
	var iterationCounter uint64

	for {
		modulo := int(iterationCounter % uint64(totalServices))
		if modulo < len(config.TrainServices) {
			log.Printf("train service: %#v", config.TrainServices[modulo])
			go CheckTrainAvailability(config.TrainServices[modulo], config.SocksUrl)
		} else {
			log.Printf("flight service: %#v", config.FlightServices[modulo-len(config.TrainServices)])
			go CheckFlightAvailability(config.FlightServices[modulo-len(config.TrainServices)], config.SocksUrl)
		}
		iterationCounter++
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func CheckTrainAvailability(service TrainService, socksAddr *string) {
	data, err := Fetch(service.URL, socksAddr)
	if err != nil {
		log.Fatalf("error happened when fetching data from alibaba: %+v", err)
	}
	var apiResponse TrainAPIResponse

	err = json.Unmarshal(data, &apiResponse)
	if err != nil {
		log.Fatalf("error happened when unmarshaling data from alibaba: %+v", err)
	}

	log.Printf("seats == %#v", lo.Map(apiResponse.Result.Departing, func(result TrainInfo, _ int) int {
		return result.Seat
	}))

	diff := service.LastIteration.Diff(apiResponse.Result.Departing)
	if len(diff) == 0 {
		log.Printf("no diff found")
		return
	}

	log.Printf("diff == %#v", diff)
	service.LastIteration.Replace(apiResponse.Result.Departing)

	for _, result := range diff {

		// layout 2023-07-22T00:00:00
		resultTime, err := time.Parse("2006-01-02T15:04:05", result.DepartureDateTime)
		if err != nil {
			log.Fatalf("error parsing date: %+v", err)
		}

		if service.DepartureHourStart != nil && resultTime.Before(time.Date(resultTime.Year(), resultTime.Month(), resultTime.Day(), int(*service.DepartureHourStart), 0, 0, 0, time.Local)) {
			continue
		}

		if service.DepartureHourEnd != nil && resultTime.After(time.Date(resultTime.Year(), resultTime.Month(), resultTime.Day(), int(*service.DepartureHourEnd), 0, 0, 0, time.Local)) {
			continue
		}

		if service.Seats != nil && *service.Seats > result.Seat {
			continue
		}

		if service.MinPriceRial != nil && *service.MinPriceRial > result.Cost {
			continue
		}

		if service.MaxPriceRial != nil && *service.MaxPriceRial < result.Cost {
			continue
		}

		if service.ShouldBeCompartment != nil && *service.ShouldBeCompartment && (!result.IsCompartment || service.CompartmentCapacity != nil && result.CompartmentCapacity != *service.CompartmentCapacity) {
			continue
		}

		resultJSON, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			log.Printf("failed to marshal result: %v", err)
		}

		err = notify.Send(
			context.Background(),
			service.Description,
			string(resultJSON),
		)
		if err != nil {
			log.Printf("failed to send telegram notification: %v", err)
		}
	}
}

func CheckFlightAvailability(service FlightService, sockAddr *string) {
	data, err := Fetch(service.URL, sockAddr)
	if err != nil {
		log.Fatalf("error happened when fetching data from alibaba: %+v", err)
	}
	var apiResponse FlightAPIResponse

	err = json.Unmarshal(data, &apiResponse)
	if err != nil {
		log.Fatalf("error happened when unmarshaling data from alibaba: %+v", err)
	}

	log.Printf("seats == %#v", lo.Map(apiResponse.Result.Departing, func(result FlightInfo, _ int) int {
		return result.Seat
	}))

	diff := service.LastIteration.Diff(apiResponse.Result.Departing)
	if len(diff) == 0 {
		log.Printf("no diff found")
		return
	}

	log.Printf("diff == %#v", diff)
	service.LastIteration.Replace(apiResponse.Result.Departing)

	for _, result := range diff {
		// layout 2023-07-22T00:00:00
		resultTime, err := time.Parse("2006-01-02T15:04:05", result.LeaveDateTime)
		if err != nil {
			log.Fatalf("error parsing date: %+v", err)
		}

		if service.DepartureHourStart != nil && resultTime.Before(time.Date(resultTime.Year(), resultTime.Month(), resultTime.Day(), int(*service.DepartureHourStart), 0, 0, 0, time.Local)) {
			continue
		}

		if service.DepartureHourEnd != nil && resultTime.After(time.Date(resultTime.Year(), resultTime.Month(), resultTime.Day(), int(*service.DepartureHourEnd), 0, 0, 0, time.Local)) {
			continue
		}

		if service.Seats != nil && *service.Seats > result.Seat {
			continue
		}

		if service.MinPriceRial != nil && *service.MinPriceRial > result.Price {
			continue
		}

		if service.MaxPriceRial != nil && *service.MaxPriceRial < result.Price {
			continue
		}

		resultJSON, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			log.Printf("failed to marshal result: %v", err)
		}

		err = notify.Send(
			context.Background(),
			service.Description,
			string(resultJSON),
		)
		if err != nil {
			log.Printf("failed to send telegram notification: %v", err)
		}
	}
}

func Fetch(url string, socksAddr *string) ([]byte, error) {
	var httpClient *http.Client

	if socksAddr != nil {
		// create a socks5 dialer
		dialer, err := proxy.SOCKS5("tcp", *socksAddr, nil, proxy.Direct)
		if err != nil {
			fmt.Fprintln(os.Stderr, "can't connect to the proxy:", err)
			os.Exit(1)
		}
		// setup a http client
		httpTransport := &http.Transport{}
		httpClient = &http.Client{Transport: httpTransport}
		// set our socks5 as the dialer
		httpTransport.Dial = dialer.Dial
	} else {
		httpClient = &http.Client{}
	}

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	return io.ReadAll(resp.Body)
}
