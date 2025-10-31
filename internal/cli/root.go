package cli

import (
	"gprxy/internal/logger"

	"github.com/spf13/cobra"
)

var rootCommand = &cobra.Command{
	Use:   "gprxy",
	Short: "Go client based Postgresql Proxy for RDS",
}

func Execute() {
	if err := rootCommand.Execute(); err != nil {
		logger.Fatal("gprxy cli startup failed : %v", err)
	}
}
