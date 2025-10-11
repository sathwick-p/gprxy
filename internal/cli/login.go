package cli

import (
	"github.com/spf13/cobra"
	"gprxy.com/internal/logger"
)

func init() {
	rootCommand.AddCommand(oidc_login)
}

var oidc_login = &cobra.Command{
	Use:   "login",
	Short: "Perform OIDC token based login via cli",
	Run:   login,
}

func login(cmd *cobra.Command, args []string) {
	logger.Printf("v1.0.0")
	// to perform login operations

}
