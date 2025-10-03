package main

import (
	"log"

	"gprxy.com/internal/config"
	"gprxy.com/internal/proxy"
	"gprxy.com/internal/tls"
)

func main() {
	tls := tls.Load()
	// Load configuration
	cfg := config.Load()

	// Create and start the proxy server
	server := proxy.NewServer(cfg, tls)

	log.Fatal(server.Start())
}
