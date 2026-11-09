package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	tokenURL = "https://tdx.transportdata.tw/auth/realms/TDXConnect/protocol/openid-connect/token"
	busURL   = "https://tdx.transportdata.tw/api/basic/v2/Bus/RealTimeByFrequency/City/NewTaipei/936?$format=JSON"
)

type Bus struct {
	PlateNumb   string `json:"PlateNumb"`
	Direction   int    `json:"Direction"`
	BusPosition struct {
		PositionLat float64 `json:"PositionLat"`
		PositionLon float64 `json:"PositionLon"`
	} `json:"BusPosition"`
	GPSTime string `json:"GPSTime"`
}

type TokenResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

var (
	client = &http.Client{Timeout: 15 * time.Second}

	tokenMu     sync.Mutex
	accessToken string
	tokenExpiry time.Time

	busMu     sync.Mutex
	busCache  []Bus
	busExpiry time.Time
)

func getToken() (string, error) {
	tokenMu.Lock()
	defer tokenMu.Unlock()

	if accessToken != "" && time.Now().Before(tokenExpiry) {
		return accessToken, nil
	}

	data := url.Values{}
	data.Set("grant_type", "client_credentials")
	data.Set("client_id", os.Getenv("TDX_CLIENT_ID"))
	data.Set("client_secret", os.Getenv("TDX_CLIENT_SECRET"))

	req, err := http.NewRequest(
		http.MethodPost,
		tokenURL,
		strings.NewReader(data.Encode()),
	)
	if err != nil {
		return "", err
	}

	req.Header.Set(
		"Content-Type",
		"application/x-www-form-urlencoded",
	)

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf(
			"TDX authentication failed: %d: %s",
			resp.StatusCode,
			string(body),
		)
	}

	var token TokenResponse

	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return "", err
	}

	if token.AccessToken == "" {
		return "", fmt.Errorf("TDX returned empty access token")
	}

	accessToken = token.AccessToken

	ttl := time.Duration(token.ExpiresIn) * time.Second
	if ttl > 60*time.Second {
		ttl -= 60 * time.Second
	}

	tokenExpiry = time.Now().Add(ttl)

	return accessToken, nil
}

func fetchBuses() ([]Bus, error) {
	token, err := getToken()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, busURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"TDX bus API returned HTTP %d",
			resp.StatusCode,
		)
	}

	var buses []Bus

	if err := json.NewDecoder(resp.Body).Decode(&buses); err != nil {
		return nil, err
	}

	result := make([]Bus, 0, len(buses))

	for _, bus := range buses {
		if bus.BusPosition.PositionLat == 0 ||
			bus.BusPosition.PositionLon == 0 {
			continue
		}

		result = append(result, bus)
	}

	return result, nil
}

func busesHandler(w http.ResponseWriter, r *http.Request) {
	busMu.Lock()
	defer busMu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")

	if time.Now().After(busExpiry) {
		buses, err := fetchBuses()
		if err != nil {
			log.Printf("fetch buses failed: %v", err)
			http.Error(
				w,
				"無法取得公車即時資料",
				http.StatusBadGateway,
			)
			return
		}

		busCache = buses
		busExpiry = time.Now().Add(30 * time.Second)
	}

	if busCache == nil {
		busCache = []Bus{}
	}

	json.NewEncoder(w).Encode(busCache)
}

func main() {
	if os.Getenv("TDX_CLIENT_ID") == "" ||
		os.Getenv("TDX_CLIENT_SECRET") == "" {
		log.Fatal("TDX credentials are required")
	}

	http.HandleFunc("/api/buses", busesHandler)

	http.Handle(
		"/",
		http.FileServer(http.Dir("./static")),
	)

	log.Println("Go Server listening on :8080")

	log.Fatal(http.ListenAndServe(":8080", nil))
}
