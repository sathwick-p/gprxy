package cli

import (
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
	"gprxy.com/internal/logger"
)

type ConnectionConfig struct {
	db_name string
	db_host string
	db_port int
}

var connectConfig ConnectionConfig

func init() {
	godotenv.Load(".env")
	flags := oidc_connect.Flags()
	flags.StringVarP(&connectConfig.db_host, "host", "s", "", "DB hostname or ip")
	flags.StringVarP(&connectConfig.db_name, "database", "d", "", "DB name")
	flags.IntVarP(&connectConfig.db_port, "port", "p", 5432, "DB port")

	oidc_connect.MarkFlagRequired("host")
	oidc_connect.MarkFlagRequired("database")

	rootCommand.AddCommand(oidc_connect)
}

var oidc_connect = &cobra.Command{
	Use:   "connect",
	Short: "Connect to the db requested by user via oidc",
	Run:   connect,
}

func (connectConfig *ConnectionConfig) Validate() error {
	if connectConfig.db_port < 1 || connectConfig.db_port > 65535 {
		return errors.New("invalid port")
	}
	if net.ParseIP(connectConfig.db_host) == nil {
		addr, err := net.LookupHost(connectConfig.db_host)
		if err != nil {
			return err
		}
		if len(addr) == 0 {
			return errors.New("host resolved to no IP addresses")
		}
	}

	target := net.JoinHostPort(connectConfig.db_host, fmt.Sprintf("%d", connectConfig.db_port))
	conn, err := net.DialTimeout("tcp", target, 2*time.Second)
	if err != nil {
		logger.Error("host unreachable on port %d: %v", connectConfig.db_port, err)
		return err
	}
	defer conn.Close()
	return nil
}
func connect(cmd *cobra.Command, args []string) {
	// Connecting to the db
	if err := connectConfig.Validate(); err != nil {
		logger.Error("configuration error: %v", err)

	}
	logger.Info("Connecting to %s:%d/%s",
		connectConfig.db_host,
		connectConfig.db_port,
		connectConfig.db_name)
}
