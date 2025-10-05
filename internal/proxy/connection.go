package proxy

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"net"
	"time"

	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"gprxy.com/internal/config"
	"gprxy.com/internal/logger"
	"gprxy.com/internal/pool"
)

// Connection represents a single client-proxy connection
type Connection struct {
	conn      net.Conn
	config    *config.Config
	poolConn  *pgxpool.Conn
	bf        *pgproto3.Frontend
	user      string
	db        string
	tlsConfig *tls.Config
	server    *Server
	key       *pgproto3.BackendKeyData
}

// handleConnection processes a single client connection in its own goroutine
func (pc *Connection) handleConnection() {
	logger.Debug("new client connection established")
	pgc := pgproto3.NewBackend(pgproto3.NewChunkReader(pc.conn), pc.conn)

	defer func() {
		if err := pc.conn.Close(); err != nil {
			logger.Error("error closing client connection: %v", err)
		}

		if pc.poolConn != nil {
			err := fullResetBeforeRelease(pc)
			if err != nil {
				logger.Error("error while releasing connection back to the pool: %v", err)
			}
			pc.poolConn.Release()
			logger.Debug("released connection back to pool")
		}
		if pc.key != nil && pc.server != nil {
			pc.server.unregisterConnection(pc.key.ProcessID, pc.key.SecretKey, pc)
		}
		logger.Info("connection closed")
	}()

	pgc, err := pc.handleStartupMessage(pgc)
	if err != nil {
		logger.Error("startup failed: %v", err)
		return
	}

	logger.Debug("entering query handling loop")
	for {
		err := pc.handleMessage(pgc)
		if err != nil {
			logger.Debug("query handling terminated: %v", err)
			return
		}
	}
}

// connectBackend establishes a connection to the backend database using connection pooling
func (pc *Connection) connectBackend(database, user string) error {
	connectionString := pc.config.BuildConnectionString(database)

	connection, err := pool.AcquireConnection(user, database, connectionString)
	if err != nil {
		return err
	}

	pc.poolConn = connection
	logger.Debug("acquired connection from pool for database: %s", database)

	pool.LogPoolStats(user, database)

	return nil
}

func cancelRequest(host string, cancel *pgproto3.CancelRequest) error {
	backendAddr := fmt.Sprintf("%s:5432", host)
	conn, err := net.DialTimeout("tcp", backendAddr, 5*time.Second)
	if err != nil {
		return fmt.Errorf("failed to connect to backend: %w", err)
	}
	defer conn.Close()

	// Build cancel request message
	buf := make([]byte, 16)
	// Message length - 16 bytes
	binary.BigEndian.PutUint32(buf[0:4], 16)

	// Cancel request code - 80877102
	binary.BigEndian.PutUint32(buf[4:8], 80877102)

	// Process ID
	binary.BigEndian.PutUint32(buf[8:12], cancel.ProcessID)

	// Secret key
	binary.BigEndian.PutUint32(buf[12:16], cancel.SecretKey)

	// Send to backend
	_, err = conn.Write(buf)

	if err != nil {
		return fmt.Errorf("failed to send cancel: %w", err)
	}

	logger.Debug("cancel request forwarded to backend: PID=%d, secret_key=%d",
		cancel.ProcessID, cancel.SecretKey)
	return nil
}

func fullResetBeforeRelease(connection *Connection) error {
	_, err := connection.poolConn.Exec(context.Background(), "ROLLBACK")
	if err != nil {
		logger.Debug("unable to rollback: %v", err)
		return err
	}
	_, err = connection.poolConn.Exec(context.Background(), "DISCARD ALL")
	if err != nil {
		logger.Debug("unable to execute discard all: %v", err)
		return err
	}
	return nil
}
