package main

import (
	"gprxy/internal/cli"
	"gprxy/internal/logger"
)

// Version is set via ldflags during build
var Version = "dev"

func main() {
	// Set version for CLI
	cli.SetVersion(Version)

	// Initialize logger from environment (LOG_LEVEL=debug or LOG_LEVEL=production)
	logger.InitFromEnv()
	cli.Execute()
}
