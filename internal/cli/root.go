package cli

import (
	"gprxy/internal/logger"

	"github.com/spf13/cobra"
)

var (
	version = "dev"
)

var rootCommand = &cobra.Command{
	Use:     "gprxy",
	Short:   "Go client based Postgresql Proxy for RDS",
	Version: version,
}

func SetVersion(v string) {
	version = v
	rootCommand.Version = v
}

func Execute() {
	if err := rootCommand.Execute(); err != nil {
		logger.Fatal("gprxy cli startup failed : %v", err)
	}
}
