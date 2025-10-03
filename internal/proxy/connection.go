package proxy

import (
	"crypto/tls"
	"log"
	"net"

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
}

// handleConnection processes a single client connection in its own goroutine
func (pc *Connection) handleConnection() {
	clientAddr := pc.conn.RemoteAddr().String()
	log.Printf("[%s] new client connection established", clientAddr)

	pgc := pgproto3.NewBackend(pgproto3.NewChunkReader(pc.conn), pc.conn)

	defer func() {
		if err := pc.conn.Close(); err != nil {
			log.Printf("[%s] error closing client connection: %v", clientAddr, err)
		}

		if pc.poolConn != nil {
			pc.poolConn.Release()
			log.Printf("[%s] released connection back to pool", clientAddr)
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
