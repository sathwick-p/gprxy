package cli

import (
	"fmt"
	"log"
	"os"

	"gprxy/internal/auth"
	"gprxy/internal/config"
	"gprxy/internal/logger"
	"gprxy/internal/proxy"
	"gprxy/internal/tls"

	"github.com/spf13/cobra"
)

func init() {
	rootCommand.AddCommand(proxyCommand)
}

var proxyCommand = &cobra.Command{
	Use:   "start",
	Short: "Start the psql proxy server",
	Run:   startProxyServer,
}

func startProxyServer(cmd *cobra.Command, args []string) {
	// Initialize authentication (JWT + Role mapping)
	auth0Tenant := os.Getenv("AUTH0_TENANT")
	audience := os.Getenv("AUDIENCE")

	if auth0Tenant == "" || audience == "" {
		logger.Fatal("Missing OAuth configuration: Set AUTH0_TENANT and AUDIENCE")
	}
	issuer := fmt.Sprintf("https://%s/", auth0Tenant)
	jwksURL := fmt.Sprintf("https://%s/.well-known/jwks.json", auth0Tenant)

	if err := auth.InitializeAuth(issuer, audience, jwksURL); err != nil {
		log.Fatalf("Failed to initialize authentication: %v", err)
	}

	tlsConfig := tls.Load()
	cfg := config.Load()
	server := proxy.NewServer(cfg, tlsConfig)
	log.Fatal(server.Start())

}
