// Command console serves the playback console (a static page with hls.js), a development tool to
// check playback by eye without a full client. The page talks to the Laterna server through the
// Connect API (CORS), and the device profile is worked out by the browser itself.
//
//	go run ./devtools/console -addr 127.0.0.1:8097
package main

import (
	_ "embed"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"
)

//go:embed index.html
var page []byte

func main() {
	addr := flag.String("addr", "127.0.0.1:8097", "listen address")
	flag.Parse()
	http.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(page)
	})
	fmt.Printf("Playback console: http://%s/\n", *addr)
	srv := &http.Server{Addr: *addr, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
