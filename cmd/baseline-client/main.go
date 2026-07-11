// Command baseline-client is the demo's contrast case: a naive HTTP POST
// of the same vitals payload, aimed at the server's /vitals endpoint. It
// deliberately has no retry logic, no chunking, and no reliability
// engineering — under the same netem-simulated loss/bandwidth cap as the
// field client, TCP's own congestion control stalls and requests time out
// or hang, which is the point: it visibly fails where the UDP+ARQ path
// survives.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"log"
	"math/rand"
	"net/http"
	"time"
)

type vitalsPayload struct {
	PatientID   uint   `json:"patient_id"`
	SeqNum      int    `json:"seq_num"`
	HeartRate   int    `json:"heart_rate"`
	SpO2        int    `json:"spo2"`
	BPSystolic  int    `json:"bp_systolic"`
	BPDiastolic int    `json:"bp_diastolic"`
	TempC       float64 `json:"temp_c"`
	Timestamp   int64  `json:"timestamp"`
}

func main() {
	serverURL := flag.String("server", "http://127.0.0.1:8080/vitals", "server HTTP endpoint to POST vitals to")
	patientID := flag.Uint("patient-id", 1001, "patient id")
	interval := flag.Duration("interval", 2*time.Second, "delay between POSTs")
	timeout := flag.Duration("timeout", 5*time.Second, "per-request HTTP timeout")
	flag.Parse()

	client := &http.Client{Timeout: *timeout}
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	seq := 0
	for {
		payload := vitalsPayload{
			PatientID:   *patientID,
			SeqNum:      seq,
			HeartRate:   70 + rng.Intn(20),
			SpO2:        94 + rng.Intn(5),
			BPSystolic:  110 + rng.Intn(20),
			BPDiastolic: 70 + rng.Intn(10),
			TempC:       36.5 + rng.Float64(),
			Timestamp:   time.Now().Unix(),
		}
		seq++

		body, _ := json.Marshal(payload)
		start := time.Now()
		resp, err := client.Post(*serverURL, "application/json", bytes.NewReader(body))
		elapsed := time.Since(start)
		if err != nil {
			log.Printf("baseline-client: seq=%d FAILED after %s: %v", payload.SeqNum, elapsed, err)
		} else {
			resp.Body.Close()
			log.Printf("baseline-client: seq=%d ok in %s (status %d)", payload.SeqNum, elapsed, resp.StatusCode)
		}

		time.Sleep(*interval)
	}
}
