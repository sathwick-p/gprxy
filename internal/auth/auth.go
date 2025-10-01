package auth

import (
	"fmt"
	"log"
	"net"
	"time"

	"github.com/jackc/pgproto3/v2"
)

// AuthenticateUser authenticates a user with PostgreSQL using a temporary connection
func AuthenticateUser(user, database, host string, startUpMessage *pgproto3.StartupMessage, clientBackend *pgproto3.Backend, clientAddr string) error {
	// Create temp connection for authentication
	backendAddress := host + ":5432"
	log.Printf("[%s] connecting to PostgreSQL at %s as %s for authentication", clientAddr, user, backendAddress)

	tempConnection, err := net.DialTimeout("tcp", backendAddress, 10*time.Second)
	if err != nil {
		log.Printf("[%s] failed to connect to PostgreSQL: %v", clientAddr, err)
		return sendErrorToClient(clientBackend, "Backend Unavailable")
	}
	defer tempConnection.Close()

	// Create frontend for temp connection
	tempFrontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(tempConnection), tempConnection)

	// Send startup message to PostgreSQL
	log.Printf("[%s] sending startup message to PostgreSQL", clientAddr)
	err = tempFrontend.Send(startUpMessage)
	if err != nil {
		log.Printf("[%s] failed to send startup message: %v", clientAddr, err)
		return sendErrorToClient(clientBackend, "Authentication failed")
	}

	// Run authentication flow
	log.Printf("[%s] starting authentication relay", clientAddr)
	err = relayAuthFlow(clientBackend, tempFrontend, clientAddr)
	if err != nil {
		log.Printf("[%s] authentication relay failed: %v", clientAddr, err)
		return err
	}

	log.Printf("[%s] authentication completed successfully", clientAddr)
	return nil
}

// relayAuthFlow handles the authentication conversation between client and backend
func relayAuthFlow(cb *pgproto3.Backend, bf *pgproto3.Frontend, clientAddr string) error {
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
			log.Printf("[%s] backend key data - process_id: %d, secret_key: %d",
				clientAddr, message.ProcessID, message.SecretKey)
			// Continue to receive more startup messages

		case *pgproto3.ParameterStatus:
			log.Printf("[%s] parameter status - %s: %s",
				clientAddr, message.Name, message.Value)
			// Continue to receive more startup messages
		}

		// If backend needs client response, get passwords etc
		if needsClientResponse(msg) {
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

// needsClientResponse determines if a backend message requires a client response
func needsClientResponse(msg pgproto3.BackendMessage) bool {
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

// sendErrorToClient sends an error message to the client
func sendErrorToClient(cb *pgproto3.Backend, msg string) error {
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
	return fmt.Errorf("%s", msg)
}
