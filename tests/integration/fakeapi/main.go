// fakeapi serves synthetic HTTPS responses inside the disposable deploy test.
// It never forwards traffic to Telegram or weather services.
package main

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"weather-sum/internal/bot"
)

type fixture struct {
	tomorrowDetailsRequested          bool
	tomorrow                          bool
	detailsRequested                  bool
	mu                                sync.Mutex
	updates                           []map[string]any
	chosen                            bool
	replies, forecasts, searches      int
	metForecasts, weatherAPIForecasts int
	sources                           bool
	weather, deleted, missing         bool
}

func update(id int, text string) map[string]any {
	return map[string]any{"update_id": id, "message": map[string]any{
		"from": map[string]any{"id": 7}, "chat": map[string]any{"id": 7, "type": "private"},
		"text": text, "entities": []map[string]any{{"type": "bot_command", "offset": 0, "length": len(strings.Fields(text)[0])}},
	}}
}

func callback(id int, data string) map[string]any {
	return map[string]any{"update_id": id, "callback_query": map[string]any{"id": strconv.Itoa(id), "from": map[string]any{"id": 7}, "message": map[string]any{"chat": map[string]any{"id": 7, "type": "private"}}, "data": data}}
}

func (f *fixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	f.mu.Lock()
	defer f.mu.Unlock()
	var body any
	switch {
	case r.URL.Path == "/status":
		body = map[string]any{"replies": f.replies, "forecasts": f.forecasts, "searches": f.searches,
			"met_forecasts": f.metForecasts, "weatherapi_forecasts": f.weatherAPIForecasts, "sources": f.sources, "tomorrow": f.tomorrow,
			"weather": f.weather, "deleted": f.deleted, "missing": f.missing}
	case r.Host == "api.telegram.org" && strings.HasSuffix(r.URL.Path, "/getMe"):
		body = map[string]any{"ok": true, "result": map[string]any{"id": 42, "username": "DeployTestBot"}}
	case r.Host == "api.telegram.org" && strings.HasSuffix(r.URL.Path, "/getUpdates"):
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		batch := []map[string]any{}
		for _, u := range f.updates {
			if u["update_id"].(int) >= offset {
				batch = append(batch, u)
			}
		}
		body = map[string]any{"ok": true, "result": batch}
	case r.Host == "api.telegram.org" && strings.HasSuffix(r.URL.Path, "/sendMessage"):
		var payload bot.Payload
		if json.NewDecoder(r.Body).Decode(&payload) != nil {
			http.Error(w, "invalid test request", 400)
			return
		}
		f.replies++
		f.tomorrow = f.tomorrow || (strings.Contains(payload.Text, "· завтра,") && strings.Contains(payload.Text, ">Open-Meteo</a>") && strings.Contains(payload.Text, ">MET Norway</a>") && strings.Contains(payload.Text, ">WeatherAPI</a>"))
		f.sources = f.sources || strings.Contains(payload.Text, "Источники: 3 из 3")
		f.weather = f.weather || (strings.Contains(payload.Text, "🌡") && !strings.Contains(payload.Text, "Источники:") && payload.ParseMode == "HTML" && payload.LinkPreviewOptions != nil && payload.LinkPreviewOptions.IsDisabled)
		f.deleted = f.deleted || strings.Contains(payload.Text, "Сохранённая локация удалена")
		f.missing = f.missing || strings.Contains(payload.Text, "Сначала выберите")
		if payload.ReplyMarkup != nil {
			for _, row := range payload.ReplyMarkup.InlineKeyboard {
				for _, button := range row {
					if !f.chosen && strings.HasPrefix(button.CallbackData, "choose:") {
						f.chosen = true
						f.updates = append(f.updates, callback(3, button.CallbackData), update(4, "/weather"))
					}
					if !f.detailsRequested && strings.HasPrefix(button.CallbackData, "details:") {
						f.detailsRequested = true
						f.updates = append(f.updates, callback(5, button.CallbackData), update(6, "/tomorrow"))
					}
					if !f.tomorrowDetailsRequested && strings.HasPrefix(button.CallbackData, "details:tomorrow:") {
						f.tomorrowDetailsRequested = true
						f.updates = append(f.updates, callback(7, button.CallbackData), update(8, "/deletelocation"))
					}
					if strings.HasPrefix(button.CallbackData, "delete:") {
						f.updates = append(f.updates, callback(9, button.CallbackData), update(10, "/location"))
					}
				}
			}
		}
		body = map[string]any{"ok": true}
	case r.Host == "api.telegram.org" && strings.HasSuffix(r.URL.Path, "/answerCallbackQuery"):
		body = map[string]any{"ok": true}
	case r.Host == "geocoding-api.open-meteo.com":
		f.searches++
		body = map[string]any{"results": []map[string]any{{"name": "Test", "latitude": 1, "longitude": 1, "timezone": "UTC"}}}
	case r.Host == "api.met.no":
		if r.UserAgent() != "weather-sum/1.0 synthetic@example.org" {
			http.Error(w, "missing test identity", 403)
			return
		}
		f.metForecasts++
		now := time.Now().UTC().Truncate(time.Hour)
		w.Header().Set("Expires", now.Add(2*time.Hour).Format(http.TimeFormat))
		w.Header().Set("Last-Modified", now.Format(http.TimeFormat))
		var series []map[string]any
		for i := 0; i < 48; i++ {
			series = append(series, map[string]any{"time": now.Add(time.Duration(i) * time.Hour).Format(time.RFC3339), "data": map[string]any{"instant": map[string]any{"details": map[string]any{"air_temperature": 6}}, "next_1_hours": map[string]any{"details": map[string]any{"precipitation_amount": 0}}}})
		}
		body = map[string]any{"properties": map[string]any{"meta": map[string]any{"units": map[string]any{"air_temperature": "celsius", "precipitation_amount": "mm"}}, "timeseries": series}}
	case r.Host == "api.weatherapi.com":
		if r.URL.Query().Get("key") != "synthetic-weatherapi-deploy" || r.URL.Query().Get("days") != "2" {
			http.Error(w, "invalid test forecast", 401)
			return
		}
		f.weatherAPIForecasts++
		now := time.Now().UTC()
		midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		var days []map[string]any
		for day := 0; day < 2; day++ {
			var hours []map[string]any
			for i := 0; i < 24; i++ {
				hours = append(hours, map[string]any{"time_epoch": midnight.Add(time.Duration(day*24+i) * time.Hour).Unix(), "temp_c": 4, "precip_mm": 0, "chance_of_rain": 0, "chance_of_snow": 0})
			}
			days = append(days, map[string]any{"hour": hours})
		}
		body = map[string]any{"forecast": map[string]any{"forecastday": days}}
	case r.Host == "api.open-meteo.com":
		f.forecasts++
		midnight, err := time.Parse("2006-01-02", r.URL.Query().Get("start_date"))
		if err != nil {
			http.Error(w, "invalid test date", 400)
			return
		}
		var stamps []int64
		var temps, mm []float64
		var probabilities []int
		for i := 0; i < 48; i++ {
			stamps = append(stamps, midnight.Add(time.Duration(i)*time.Hour).Unix())
			temps = append(temps, 5)
			mm = append(mm, 0)
			probabilities = append(probabilities, 1)
		}
		body = map[string]any{"hourly": map[string]any{"time": stamps, "temperature_2m": temps, "precipitation": mm, "precipitation_probability": probabilities}}
	default:
		http.Error(w, fmt.Sprintf("unexpected test endpoint: %s", r.URL.Path), 404)
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}

func main() {
	status := flag.Bool("status", false, "check fixture readiness and print counters")
	flag.Parse()
	if *status {
		pem, err := os.ReadFile("/fixture/cert.pem")
		if err != nil {
			log.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			log.Fatal("invalid fixture certificate")
		}
		client := &http.Client{Timeout: time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}}
		res, err := client.Get("https://127.0.0.1/status")
		if err != nil {
			log.Fatal(err)
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			log.Fatal("fixture not ready")
		}
		if _, err := io.Copy(os.Stdout, res.Body); err != nil {
			log.Fatal(err)
		}
		return
	}
	f := &fixture{updates: []map[string]any{update(1, "/start"), update(2, "/setlocation Test")}}
	s := &http.Server{Addr: ":443", Handler: f, ReadHeaderTimeout: 2 * time.Second}
	log.Fatal(s.ListenAndServeTLS("/fixture/cert.pem", "/fixture/key.pem"))
}
