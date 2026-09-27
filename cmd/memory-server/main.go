package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/hchw/mengpo/internal/observability"
)

func main() {
	metrics := &observability.Metrics{}
	mux := http.NewServeMux()
	mux.Handle("/", observability.HealthHandler(nil))
	mux.Handle("/metrics", metrics.Handler())

	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	server := &http.Server{
		Addr:              addr,
		Handler:           metrics.Middleware(observability.NewLogger(nil), mux),
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("memory server scaffold listening on %s", server.Addr)
	log.Fatal(server.ListenAndServe())
}
