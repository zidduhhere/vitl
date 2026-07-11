// Command netcontrol serves a local slider UI for setting packet loss and
// bandwidth caps on a macOS network interface via dnctl/pf (dummynet).
// It requires macOS (dnctl/pfctl/osascript) and is for local dev/testing
// only.
package main

import (
	"flag"
	"log"
	"net/http"
)

func main() {
	addr := flag.String("addr", ":8090", "listen address for the netcontrol UI/API")
	staticDir := flag.String("static", "network-sim/macos-control", "directory containing the control panel static files")
	flag.Parse()

	mux := newMux(*staticDir)
	log.Printf("netcontrol listening on %s (serving %s)", *addr, *staticDir)
	log.Fatal(http.ListenAndServe(*addr, mux))
}
