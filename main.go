package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"time"

	"github.com/jackc/pgproto3/v2"
)

type gprxyConn struct {
	conn        net.Conn
	addr        string
	backendconn net.Conn
	bf          *pgproto3.Frontend
	user        string // Store the connected user
}

func runGprxy() {
	host := os.Getenv("DB_HOST")
	if host == "" {
		host = "localhost" // Default fallback
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
		if pc.backendconn != nil {
			if err := pc.backendconn.Close(); err != nil {
				log.Printf("[%s] error closing backend connection: %v", clientAddr, err)
			}
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
		log.Printf("[%s] [%s] simple query: %s", clientAddr, pc.user, query.String)
	case *pgproto3.Parse:
		log.Printf("[%s] [%s] parse statement '%s': %s", clientAddr, pc.user, query.Name, query.Query)
	case *pgproto3.Bind:
		log.Printf("[%s|%s] bind portal '%s' to statement '%s'", clientAddr, pc.user, query.DestinationPortal, query.PreparedStatement)
	case *pgproto3.Execute:
		log.Printf("[%s|%s] execute portal: %s", clientAddr, pc.user, query.Portal)
	case *pgproto3.Terminate:
		log.Printf("[%s|%s] client disconnecting gracefully", clientAddr, pc.user)
		return fmt.Errorf("client terminated")
	default:
		log.Printf("[%s|%s] message type: %T", clientAddr, pc.user, query)
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

		// 1. Connect to the backend PostgreSQL server
		start := time.Now()
		backendConnection, err := pc.connectBackend(database, user)
		if err != nil {
			log.Printf("[%s] failed to connect to backend: %v", clientAddr, err)
			return pc.sendErrorToClient(pgconn, "Database unavailable")
		}
		log.Printf("[%s] backend connection established in %v", clientAddr, time.Since(start))

		// 2. Frontend to communicate with backend
		bf := pgproto3.NewFrontend(pgproto3.NewChunkReader(backendConnection), backendConnection)

		// 3. Forward startup message to backend
		err = bf.Send(msg)
		if err != nil {
			log.Printf("[%s] failed to send startup message to backend: %v", clientAddr, err)
			return pc.sendErrorToClient(pgconn, "Backend connection failed")
		}

		// 4. Authentication flow
		log.Printf("[%s] starting authentication flow", clientAddr)
		err = pc.relayAuthFlow(pgconn, bf)
		if err != nil {
			log.Printf("[%s] authentication flow failed: %v", clientAddr, err)
			return err
		}

		pc.backendconn = backendConnection
		pc.bf = bf
		pc.user = user // Store user for logging

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

func (pc *gprxyConn) connectBackend(database, user string) (net.Conn, error) {
	backendAddr := pc.addr + ":5432"
	conn, err := net.DialTimeout("tcp", backendAddr, 10*time.Second)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to backend %s: %w", backendAddr, err)
	}
	return conn, nil
}

func (pc *gprxyConn) sendErrorToClient(cb *pgproto3.Backend, msg string) error {
	errMsg := &pgproto3.ErrorResponse{
		Severity: "FATAL",
		Code:     "08006",
		Message:  msg,
	}
	return cb.Send(errMsg)
}

// relayAuthFlow handles the authentication conversation between client and backend
func (pc *gprxyConn) relayAuthFlow(cb *pgproto3.Backend, bf *pgproto3.Frontend) error {
	clientAddr := pc.conn.RemoteAddr().String()

	for {
		// Read from backend
		msg, err := bf.Receive()
		if err != nil {
			// Proxy error, can't communicate with backend
			return pc.sendErrorToClient(cb, "Lost connection to backend")
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
