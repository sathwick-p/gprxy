package cli

import (
	"log"

	"github.com/spf13/cobra"
	"gprxy.com/internal/config"
	"gprxy.com/internal/proxy"
	"gprxy.com/internal/tls"
)

func init(){
	rootCommand.AddCommand(proxyCommand)
}

var proxyCommand = &cobra.Command{
	Use: "start",
	Short : "Start the psql proxy server",
	Run : startProxyServer,
}

func startProxyServer(cmd *cobra.Command, args []string){
	tlsConfig := tls.Load()
	cfg := config.Load()
	server := proxy.NewServer(cfg,tlsConfig)
	log.Fatal(server.Start())

}