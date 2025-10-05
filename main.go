package main

import (
	"log"

	"gprxy.com/internal/config"
	"gprxy.com/internal/logger"
	"gprxy.com/internal/proxy"
	"gprxy.com/internal/tls"
)

func main() {
	// Initialize logger from environment (LOG_LEVEL=debug or LOG_LEVEL=production)
	logger.InitFromEnv()

	tls := tls.Load()
	// Load configuration
	cfg := config.Load()

	// Create and start the proxy server
	server := proxy.NewServer(cfg, tls)

	log.Fatal(server.Start())
}
