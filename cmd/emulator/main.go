package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"time"
)

func main() {
	port := flag.Int("port", 8081, "Port to run the emulator on")
	flag.Parse()

	mux := http.NewServeMux()

	mux.HandleFunc("/api/emulator/vitals", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Cache-Control")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
			return
		}

		// Flush headers immediately so the browser knows the connection is established
		flusher.Flush()

		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-r.Context().Done():
				return
			case <-ticker.C:
				vitals := map[string]interface{}{
					"heartRate":   60 + rand.Intn(40),
					"spO2":        95 + rand.Intn(5),
					"bpSystolic":  110 + rand.Intn(30),
					"bpDiastolic": 70 + rand.Intn(20),
					"tempC":       36.0 + rand.Float64(),
					"timestamp":   time.Now().Unix(),
				}
				data, _ := json.Marshal(vitals)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			}
		}
	})

	addr := fmt.Sprintf(":%d", *port)
	log.Printf("Hardware emulator listening on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("Emulator failed: %v", err)
	}
}
