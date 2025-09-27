package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgproto3/v2"
	"github.com/joho/godotenv"

	"github.com/jackc/pgx/v5/pgxpool"
)

type gprxyConn struct {
	conn     net.Conn
	addr     string
	poolConn *pgxpool.Conn
	bf       *pgproto3.Frontend
	user     string
	db       string
}

var (
	poolManager = make(map[string]*pgxpool.Pool)
	poolMutex   sync.RWMutex
)

func runGprxy() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Println("No .env file found, using system environment")
	}
	host := os.Getenv("DB_HOST")
	if host == "" {
		host = "localhost"
	}

	ln, err := net.Listen("tcp", host+":7777")
	if err != nil {
		log.Fatalf("failed to start proxy server: %v", err)
	}

	log.Printf("PostgreSQL proxy listening on %s", ln.Addr())

	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Printf("failed to accept connection: %v", err)
			continue // Don't kill server on accept error
		}

		pc := &gprxyConn{
			conn: conn,
			addr: host,
		}
		go pc.handleConnection()
	}
}

func main() {
	runGprxy()
}

// handleConnection processes a single client connection in its own goroutine
func (pc *gprxyConn) handleConnection() {
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

func (pc *gprxyConn) handleMessage(client *pgproto3.Backend) error {
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

	// Handle termination
	if _, ok := msg.(*pgproto3.Terminate); ok {
		return fmt.Errorf("connection terminated")
	}

	// Relay backend responses back to client
	return pc.relayBackendResponse(client)
}

func (pc *gprxyConn) relayBackendResponse(client *pgproto3.Backend) error {
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

func (pc *gprxyConn) handleStartupMessage(pgconn *pgproto3.Backend) error {
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

		// TODO : Azure SSO

		// For now, accept all user requests and blindly send request to the backend
		// not doing custom auth, auth will be taken care by pg_hba.conf

		// 1. Authenticate user with postgresql first
		err := pc.authUserWithPSQL(user, database, msg, pgconn)
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

func (pc *gprxyConn) authUserWithPSQL(user, database string, startUpMessage *pgproto3.StartupMessage, clientBackend *pgproto3.Backend) error {
	clientAddr := pc.conn.RemoteAddr().String()

	// Create temp connection for authentication
	backendAddress := pc.addr + ":5432"
	log.Printf("[%s] connecting to PostgreSQL at %s for authentication", clientAddr, backendAddress)

	tempConnection, err := net.DialTimeout("tcp", backendAddress, 10*time.Second)
	if err != nil {
		log.Printf("[%s] failed to connect to PostgreSQL: %v", clientAddr, err)
		return pc.sendErrorToClient(clientBackend, "Backend Unavailable")
	}
	defer tempConnection.Close()

	// Create frontend for temp connection
	tempFrontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(tempConnection), tempConnection)

	// Send startup message to PostgreSQL
	log.Printf("[%s] sending startup message to PostgreSQL", clientAddr)
	err = tempFrontend.Send(startUpMessage)
	if err != nil {
		log.Printf("[%s] failed to send startup message: %v", clientAddr, err)
		return pc.sendErrorToClient(clientBackend, "Authentication failed")
	}

	// Run authentication flow
	log.Printf("[%s] starting authentication relay", clientAddr)
	err = pc.relayAuthFlow(clientBackend, tempFrontend)
	if err != nil {
		log.Printf("[%s] authentication relay failed: %v", clientAddr, err)
		return err
	}

	log.Printf("[%s] authentication completed successfully", clientAddr)
	return nil
}

func (pc *gprxyConn) connectBackend(database, user string) error {
	// backendAddr := pc.addr + ":5432"
	// conn, err := net.DialTimeout("tcp", backendAddr, 10*time.Second)
	// if err != nil {
	// 	return nil, fmt.Errorf("failed to connect to backend %s: %w", backendAddr, err)
	// }
	// return conn, nil
	connPool, err := pc.getOrCreatePool(database)
	if err != nil {
		return fmt.Errorf("error while creating connection to the database: %w", err)
	}

	// Acquire a connection from the pool
	connection, err := connPool.Acquire(context.Background())
	if err != nil {
		return fmt.Errorf("error while acquiring connection from the database pool: %w", err)
	}

	// Test connection
	err = connection.Ping(context.Background())
	if err != nil {
		connection.Release() // Release on error
		return fmt.Errorf("could not ping database: %w", err)
	}

	pc.poolConn = connection
	log.Printf("[%s] acquired connection from pool for database: %s", pc.addr, database)

	stats := connPool.Stat()
	log.Printf("Pool stats - Total: %d, Acquired: %d, Idle: %d",
		stats.TotalConns(), stats.AcquiredConns(), stats.IdleConns())
	return nil
}

func (pc *gprxyConn) sendErrorToClient(cb *pgproto3.Backend, msg string) error {
	errMsg := &pgproto3.ErrorResponse{
		Severity: "FATAL",
		Code:     "08006",
		Message:  msg,
	}
	err := cb.Send(errMsg)
	if err != nil {
		return fmt.Errorf("failed to send error to client: %w", err)
	}
	ready := pgproto3.ReadyForQuery{TxStatus: 'I'}
	err = cb.Send(&ready)
	if err != nil {
		return fmt.Errorf("failed to send ready to client: %w", err)
	}
	return fmt.Errorf(msg)
}

// relayAuthFlow handles the authentication conversation between client and backend
func (pc *gprxyConn) relayAuthFlow(cb *pgproto3.Backend, bf *pgproto3.Frontend) error {
	clientAddr := pc.conn.RemoteAddr().String()

	for {
		// Read from backend
		msg, err := bf.Receive()
		if err != nil {
			// Proxy error, can't communicate with backend
			return fmt.Errorf("lost connection to backend: %w", err)
		}

		// Forward backend's message to the client - either error or success
		err = cb.Send(msg)
		if err != nil {
			return fmt.Errorf("failed to send to client: %w", err)
		}

		switch message := msg.(type) {
		case *pgproto3.ErrorResponse:
			log.Printf("[%s] authentication failed - severity: %s, code: %s, message: %s",
				clientAddr, message.Severity, message.Code, message.Message)
			return fmt.Errorf("authentication failed: %s", message.Message)

		case *pgproto3.ReadyForQuery:
			log.Printf("[%s] authentication successful, ready for queries", clientAddr)
			return nil

		case *pgproto3.AuthenticationOk:
			log.Printf("[%s] authentication OK received", clientAddr)
			// Continue to receive more startup messages

		case *pgproto3.BackendKeyData:
			log.Printf("[%s] backend key data - process: %d, secret: %d",
				clientAddr, message.ProcessID, message.SecretKey)
			// Continue to receive more startup messages

		case *pgproto3.ParameterStatus:
			log.Printf("[%s] parameter status - %s: %s",
				clientAddr, message.Name, message.Value)
			// Continue to receive more startup messages
		}

		// If backend needs client response, get passwords etc
		if pc.needsClientResponse(msg) {
			clientMsg, err := cb.Receive()
			if err != nil {
				return fmt.Errorf("client disconnected: %w", err)
			}
			err = bf.Send(clientMsg)
			if err != nil {
				return fmt.Errorf("backend error: %w", err)
			}
		}
	}
}

func (pc *gprxyConn) needsClientResponse(msg pgproto3.BackendMessage) bool {
	switch msg.(type) {
	case *pgproto3.AuthenticationMD5Password,
		*pgproto3.AuthenticationCleartextPassword,
		*pgproto3.AuthenticationSASL,
		*pgproto3.AuthenticationSASLContinue,
		*pgproto3.AuthenticationSASLFinal:
		return true
	default:
		return false
	}
}

func (pc *gprxyConn) getOrCreatePool(database string) (*pgxpool.Pool, error) {

	const defaultMaxConns = int32(7)
	const defaultMinConns = int32(0)
	const defaultMaxConnLifetime = time.Hour
	const defaultMaxConnIdleTime = time.Minute * 30
	const defaultHealthCheckPeriod = time.Minute
	const defaultConnectTimeout = time.Second * 5

	poolMutex.RLock() // read lock for go routines trying to read the pool
	pool, exists := poolManager[database]
	poolMutex.RUnlock()

	if exists {
		return pool, nil
	}

	poolMutex.Lock()
	defer poolMutex.Unlock()

	// checking to see if it exists

	if pool, exists := poolManager[database]; exists {
		return pool, nil
	}

	// else if it does not exist then

	connectionString := pc.buildConnectionString(database)
	config, err := pgxpool.ParseConfig(connectionString)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config: %w", err)
	}

	config.MaxConns = defaultMaxConns
	config.MinConns = defaultMinConns
	config.MaxConnLifetime = defaultMaxConnLifetime
	config.MaxConnIdleTime = defaultMaxConnIdleTime
	config.HealthCheckPeriod = defaultHealthCheckPeriod
	config.ConnConfig.ConnectTimeout = defaultConnectTimeout

	pool, err = pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, fmt.Errorf("failed to create pool: %w", err)
	}
	poolManager[database] = pool
	log.Printf("Created a new connection pool for database: %s", database)
	return pool, nil

}
func (pc *gprxyConn) buildConnectionString(database string) string {
	serviceUser := os.Getenv("GPRXY_USER")
	servicePass := os.Getenv("GPRXY_PASS")
	host := pc.addr
	port := "5432"
	db := database
	if serviceUser == "" {
		log.Fatal("GPRXY_USER environment variable is required")
	}
	if servicePass == "" {
		log.Fatal("GPRXY_PASS environment variable is required")
	}
	if host == "" {
		host = "localhost"
	}
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%s/%s",
		serviceUser,
		servicePass,
		host,
		port,
		db,
	)
}
