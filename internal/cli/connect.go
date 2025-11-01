package cli

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sync"
	"time"

	"gprxy/internal/logger"
	"gprxy/internal/tls"

	"github.com/jackc/pgproto3/v2"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
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
		return logger.Errorf("host unreachable on port %d: %v", connectConfig.db_port, err)
	}
	defer conn.Close()
	return nil
}

func getCreds() (*SavedCreds, error) {
	creds, err := loadCreds()
	if err != nil {
		logger.Error("Unable to load creds", err)
	}
	logger.Debug("loaded access token from ~/.gprxy/credentials")

	if time.Until(creds.ExpiresAt) < 30*time.Minute {
		logger.Info("Token expiring, fetching refresh token")
		newCreds, err := getRefreshToken()
		if err != nil {
			return nil, logger.Errorf("Token refresh failed: %v", err)
		}
		logger.Info("token refreshed successfully")
		return newCreds, nil
	}

	logger.Info("Using existing valid token (expires in %v)", time.Until(creds.ExpiresAt).Round(time.Minute))
	return creds, nil
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

	// read auth file if it exists
	creds, err := getCreds()
	if err != nil {
		logger.Fatal("Failed to get credentials: %v", err)
		return
	}
	proxy_url := net.JoinHostPort(os.Getenv("PROXY_URL"), "7777")
	conn, err := net.DialTimeout("tcp", proxy_url, 5*time.Second)
	if err != nil {
		logger.Error("trouble establishing connnection to the proxy: %v", err)
	}
	defer conn.Close()

	conn, err = tls.UpgradeToTLS(conn, os.Getenv("PROXY_URL"))
	if err != nil {
		logger.Fatal("TLS upgrade failed: %v", err)
	}

	proxyConnection := pgproto3.NewFrontend(pgproto3.NewChunkReader(conn), conn)
	startupMessage := &pgproto3.StartupMessage{
		ProtocolVersion: pgproto3.ProtocolVersionNumber,
		Parameters: map[string]string{
			"application_name": "psql",
			"client_encoding":  "UTF8",
			"database":         connectConfig.db_name,
			"user":             creds.UserInfo.Email,
		},
	}
	err = proxyConnection.Send(startupMessage)
	if err != nil {
		logger.Error("failed to send startup message: %v", err)
	}

	msg, _ := proxyConnection.Receive()

	logger.Debug("proxy sent back: %v", msg)

	pmsg := &pgproto3.PasswordMessage{
		Password: creds.AccessToken,
	}
	err = proxyConnection.Send(pmsg)
	if err != nil {
		logger.Fatal("failed to send password message: %v", err)
		return
	}

	// Wait for authentication to complete and handle all protocol messages
	logger.Debug("Waiting for authentication response...")

	for {
		msg, err := proxyConnection.Receive()
		if err != nil {
			logger.Fatal("Failed to receive from proxy: %v", err)
			return
		}

		switch v := msg.(type) {
		case *pgproto3.AuthenticationOk:
			logger.Info("Authentication successful.")

		case *pgproto3.ParameterStatus:
			logger.Debug("Parameter: %s = %s", v.Name, v.Value)

		case *pgproto3.BackendKeyData:
			logger.Debug("Backend key data: PID=%d, SecretKey=%d", v.ProcessID, v.SecretKey)

		case *pgproto3.ReadyForQuery:
			logger.Info("Connection established.")
			logger.Info("Database: %s, User: %s", connectConfig.db_name, creds.UserInfo.Email)
			logger.Info("Connection is ready for queries (TxStatus: %c)", v.TxStatus)
			logger.Info("\nStarting interactive session...")

			// Start interactive session - relay messages bidirectionally
			err := startInteractiveSession(conn, proxyConnection)
			if err != nil {
				logger.Error("Interactive session error: %v", err)
			}
			return

		case *pgproto3.ErrorResponse:
			logger.Fatal("Authentication failed: [%s] %s", v.Code, v.Message)
			return

		default:
			logger.Debug("Received message: %T", msg)
		}
	}
}

// startInteractiveSession creates a bidirectional relay between stdin/stdout and the proxy
func startInteractiveSession(rawConn net.Conn, frontend *pgproto3.Frontend) error {
	logger.Info("Interactive session started. Press Ctrl+C to exit.")

	var wg sync.WaitGroup
	errChan := make(chan error, 2)

	// Goroutine 1: Copy from stdin to proxy (client -> proxy)
	wg.Add(1)
	go func() {
		defer wg.Done()
		written, err := io.Copy(rawConn, os.Stdin)
		if err != nil {
			errChan <- fmt.Errorf("stdin->proxy error: %w", err)
			return
		}
		logger.Debug("Stdin closed, wrote %d bytes", written)
	}()

	// Goroutine 2: Copy from proxy to stdout (proxy -> client)
	wg.Add(1)
	go func() {
		defer wg.Done()
		written, err := io.Copy(os.Stdout, rawConn)
		if err != nil {
			errChan <- fmt.Errorf("proxy->stdout error: %w", err)
			return
		}
		logger.Debug("Proxy connection closed, wrote %d bytes", written)
	}()

	// Wait for either goroutine to finish or error
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// Return the first error (if any)
	for err := range errChan {
		if err != nil {
			return err
		}
	}

	logger.Info("Interactive session ended")
	return nil
}
