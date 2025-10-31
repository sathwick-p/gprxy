package cli

import (
	"gprxy/internal/logger"

	"github.com/spf13/cobra"
)

func init() {
	rootCommand.AddCommand(version)
}

var version = &cobra.Command{
	Use:   "version",
	Short: "prints currently installed version of gprxy",
	Run:   printVersion,
}

func printVersion(cmd *cobra.Command, args []string) {
	logger.Printf("v1.0.0")

}
