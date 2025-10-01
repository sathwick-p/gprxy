package proxy

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/jackc/pgproto3/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"gprxy.com/internal/auth"
	"gprxy.com/internal/config"
	"gprxy.com/internal/pool"
)

// Connection represents a single client-proxy connection
type Connection struct {
	conn     net.Conn
	config   *config.Config
	poolConn *pgxpool.Conn
	bf       *pgproto3.Frontend
	user     string
	db       string
}

// Server represents the proxy server
type Server struct {
	config *config.Config
}

// NewServer creates a new proxy server
func NewServer(cfg *config.Config) *Server {
	return &Server{
		config: cfg,
	}
}

// Start starts the proxy server
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.config.Host+":"+s.config.Port)
	if err != nil {
		return fmt.Errorf("failed to start proxy server: %w", err)
	}

	log.Printf("PostgreSQL proxy listening on %s", ln.Addr())

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("failed to accept connection: %v", err)
			continue // Don't kill server on accept error
		}

		pc := &Connection{
			conn:   conn,
			config: s.config,
		}
		go pc.handleConnection()
	}
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

		// release pool connection back to pool
		if pc.poolConn != nil {
			pc.poolConn.Release()
			log.Printf("[%s] released connection back to pool", clientAddr)
		}
		log.Printf("[%s] connection closed", clientAddr)
	}()

	err := pc.handleStartupMessage(pgc)
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

// handleStartupMessage handles the initial client startup message
func (pc *Connection) handleStartupMessage(pgconn *pgproto3.Backend) error {
	clientAddr := pc.conn.RemoteAddr().String()

	startupMessage, err := pgconn.ReceiveStartupMessage()
	if err != nil {
		return fmt.Errorf("error receiving startup message: %w", err)
	}

	log.Printf("[%s] received startup message type: %T", clientAddr, startupMessage)

	switch msg := startupMessage.(type) {
	case *pgproto3.StartupMessage:
		user := msg.Parameters["user"]
		database := msg.Parameters["database"]
		appName := msg.Parameters["application_name"]
		log.Printf("[%s] connection request - user: %s, database: %s, app: %s",
			clientAddr, user, database, appName)

		// 1. Authenticate user with postgresql first
		err := auth.AuthenticateUser(user, database, pc.config.Host, msg, pgconn, clientAddr)
		if err != nil {
			log.Printf("[%s] authentication failed for user %s: %v", clientAddr, user, err)
			return err
		}
		log.Printf("[%s] user %s authenticated successfully", clientAddr, user)

		// 2. Connect to the backend PostgreSQL server
		start := time.Now()
		err = pc.connectBackend(database, user)
		if err != nil {
			log.Printf("[%s] failed to connect to backend: %v", clientAddr, err)
			return pc.sendErrorToClient(pgconn, "Database unavailable")
		}
		log.Printf("[%s] backend connection established in %v", clientAddr, time.Since(start))

		// Get the underlying net conn connection from pgpool connection for protocol communication
		underlyingConn := pc.poolConn.Conn().PgConn().Conn()

		// 3. Setup query forwarding
		bf := pgproto3.NewFrontend(pgproto3.NewChunkReader(underlyingConn), underlyingConn)
		pc.bf = bf
		pc.user = user // Store user for logging
		pc.db = database

	case *pgproto3.SSLRequest:
		log.Printf("[%s] SSL request received, rejecting (not implemented)", clientAddr)
		_, err := pc.conn.Write([]byte{'N'})
		if err != nil {
			return fmt.Errorf("failed to send SSL rejection: %w", err)
		}
		return pc.handleStartupMessage(pgconn)

	case *pgproto3.CancelRequest:
		log.Printf("[%s] cancel request received (not implemented)", clientAddr)
		return fmt.Errorf("cancel requests not implemented")
	}

	return nil
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

// handleMessage handles incoming client messages after authentication
func (pc *Connection) handleMessage(client *pgproto3.Backend) error {
	// Receive message from client
	msg, err := client.Receive()
	if err != nil {
		return fmt.Errorf("client receive error: %w", err)
	}

	clientAddr := pc.conn.RemoteAddr().String()

	switch query := msg.(type) {
	case *pgproto3.Query:
		log.Printf("[%s] [%s] QUERY: %s", clientAddr, pc.user, query.String)

	case *pgproto3.Parse:
		log.Printf("[%s] [%s] PARSE: statement='%s' query='%s'", clientAddr, pc.user, query.Name, query.Query)

	case *pgproto3.Describe:
		objectType := "statement"
		if query.ObjectType == 'P' {
			objectType = "portal"
		}
		log.Printf("[%s] [%s] DESCRIBE: %s='%s'", clientAddr, pc.user, objectType, query.Name)

	case *pgproto3.Bind:
		paramCount := len(query.Parameters)
		log.Printf("[%s] [%s] BIND: portal='%s' statement='%s' params=%d", clientAddr, pc.user, query.DestinationPortal, query.PreparedStatement, paramCount)

	case *pgproto3.Execute:
		maxRows := "unlimited"
		if query.MaxRows > 0 {
			maxRows = fmt.Sprintf("%d", query.MaxRows)
		}
		log.Printf("[%s] [%s] EXECUTE: portal='%s' max_rows=%s", clientAddr, pc.user, query.Portal, maxRows)

	case *pgproto3.Sync:
		log.Printf("[%s] [%s] SYNC: transaction boundary", clientAddr, pc.user)

	case *pgproto3.Terminate:
		log.Printf("[%s] [%s] TERMINATE: client disconnecting gracefully", clientAddr, pc.user)
		return fmt.Errorf("client terminated")

	default:
		log.Printf("[%s] [%s] UNKNOWN_MESSAGE: %T", clientAddr, pc.user, query)
	}

	// Forward message to backend
	err = pc.bf.Send(msg)
	if err != nil {
		return fmt.Errorf("unable to send query to backend: %w", err)
	}
	// No Flush available on pgproto3 Frontend; Send writes directly

	// Handle termination
	if _, ok := msg.(*pgproto3.Terminate); ok {
		return fmt.Errorf("connection terminated")
	}

	// Relay backend responses back to client
	return pc.relayBackendResponse(client)
}

// relayBackendResponse relays backend responses back to the client
func (pc *Connection) relayBackendResponse(client *pgproto3.Backend) error {
	clientAddr := pc.conn.RemoteAddr().String()

	for {
		// Get response from the backend
		msg, err := pc.bf.Receive()
		if err != nil {
			return fmt.Errorf("backend receive error: %w", err)
		}

		err = client.Send(msg)
		if err != nil {
			return fmt.Errorf("client send error: %w", err)
		}
		// No Flush available on pgproto3 Backend; Send writes directly

		switch msgType := msg.(type) {
		case *pgproto3.ReadyForQuery:
			log.Printf("[%s] query completed, ready for next (status: %c)",
				clientAddr, msgType.TxStatus)
			return nil
		case *pgproto3.ErrorResponse:
			log.Printf("[%s] query error: %s (code: %s)",
				clientAddr, msgType.Message, msgType.Code)
			// Don't return here! Continue reading until ReadyForQuery
		case *pgproto3.CommandComplete:
			log.Printf("[%s] command completed: %s",
				clientAddr, msgType.CommandTag)
			// Continue reading more messages
		}

		// Continue reading more response messages (DataRow, RowDescription, etc.)
	}
}

// sendErrorToClient sends an error message to the client
func (pc *Connection) sendErrorToClient(cb *pgproto3.Backend, msg string) error {
	errMsg := &pgproto3.ErrorResponse{
		Severity: "FATAL",
		Code:     "08006",
		Message:  msg,
	}
	err := cb.Send(errMsg)
	if err != nil {
		return fmt.Errorf("failed to send error to client: %w", err)
	}
	// No Flush available on pgproto3 Backend; Send writes directly
	return fmt.Errorf("%s", msg)
}
