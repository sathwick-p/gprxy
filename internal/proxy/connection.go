package proxy

import (
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"log"
	"net"
	"time"

	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"gprxy.com/internal/config"
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
	clientAddr := pc.conn.RemoteAddr().String()
	log.Printf("[%s] new client connection/creating established")
	pgc := pgproto3.NewBackend(pgproto3.NewChunkReader(pc.conn), pc.conn)

	defer func() {
		if err := pc.conn.Close(); err != nil {
			log.Printf("[%s] error closing client connection: %v", clientAddr, err)
		}

		if pc.poolConn != nil {
			pc.poolConn.Release()
			log.Printf("[%s] released connection back to pool", clientAddr)
		}
		if pc.key != nil && pc.server != nil {
			pc.server.unregisterConnection(pc.key.ProcessID, pc.key.SecretKey, pc)
		}
		log.Printf("[%s] connection closed", clientAddr)
	}()

	pgc, err := pc.handleStartupMessage(pgc)
	if err != nil {
		log.Printf("[%s] startup failed: %v", clientAddr, err)
		return
	}

	log.Printf("[%s] entering query handling loop", clientAddr)
	for {
		err := pc.handleMessage(pgc)
		if err != nil {
			log.Printf("[%s] query handling error: %v", clientAddr, err)
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
	log.Printf("[%s] acquired connection from pool for database: %s", pc.config.Host, database)

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
	// message length - 16 bytes
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

	log.Printf("Cancel forwarded to backend: PID=%d, Key=%d",
		cancel.ProcessID, cancel.SecretKey)
	return nil
}
