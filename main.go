package main

import (
	"log"

	"gprxy.com/internal/config"
	"gprxy.com/internal/proxy"
)

func main() {
	// Load configuration
	cfg := config.Load()

	// Create and start the proxy server
	server := proxy.NewServer(cfg)

	log.Fatal(server.Start())
}
