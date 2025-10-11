package cli

import (
	"github.com/spf13/cobra"
	"gprxy.com/internal/logger"
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
