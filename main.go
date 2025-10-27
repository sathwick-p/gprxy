package main

import (
	"gprxy.com/internal/cli"
	"gprxy.com/internal/logger"
)

func main() {
	// Initialize logger from environment (LOG_LEVEL=debug or LOG_LEVEL=production)
	logger.InitFromEnv()
	cli.Execute()
}
