package main

import (
	"log"
	"net/http"

	"influx-bouncer/internal/gateway"
)

func main() {
	cfg, err := gateway.LoadConfig()
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	srv := gateway.NewServer(cfg)
	log.Printf("influx-gateway listening on %s", cfg.HTTPAddr)
	if err := http.ListenAndServe(cfg.HTTPAddr, srv.Handler()); err != nil {
		log.Fatalf("server failed: %v", err)
	}
}
