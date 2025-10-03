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
	log.Printf("[%s] connecting to PostgreSQL at %s as %s for authentication", clientAddr, backendAddress, user)

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
	// No Flush available on pgproto3 Frontend; Send writes directly

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

		// log.Printf("[%s] backend->client message: %T", clientAddr, msg)

		// Forward backend's message to the client - either error or success
		err = cb.Send(msg)
		if err != nil {
			return fmt.Errorf("failed to send to client: %w", err)
		}
		// No Flush on Backend; Send writes directly

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
			// log.Printf("[%s] parameter status - %s: %s",
			// 	clientAddr, message.Name, message.Value)
			// Continue to receive more startup messages
		}

		// Handle authentication challenges that need client response
		switch authMsg := msg.(type) {
		case *pgproto3.AuthenticationSASL:
			// SCRAM-SHA-256 multi-step authentication
			if err := handleSASLAuth(cb, bf, clientAddr); err != nil {
				return err
			}
		case *pgproto3.AuthenticationSASLContinue:
			// Continue SCRAM authentication
			if err := handleSASLContinue(cb, bf, clientAddr); err != nil {
				return err
			}
		case *pgproto3.AuthenticationSASLFinal:
			// Final SCRAM message (no client response needed)
			log.Printf("[%s] SASL authentication final step", clientAddr)
			// Server will send AuthenticationOk next
		case *pgproto3.AuthenticationMD5Password, *pgproto3.AuthenticationCleartextPassword:
			// Simple password authentication
			if err := handlePasswordAuth(cb, bf, clientAddr, authMsg); err != nil {
				return err
			}
		}
	}
}

// handleSASLAuth handles SCRAM-SHA-256 authentication initial step
func handleSASLAuth(cb *pgproto3.Backend, bf *pgproto3.Frontend, clientAddr string) error {
	log.Printf("[%s] awaiting SASL initial response from client", clientAddr)
	clientMsg, err := cb.Receive()
	if err != nil {
		return fmt.Errorf("client disconnected during SASL: %w", err)
	}

	// Client should send SASLInitialResponse, but might send PasswordMessage (protocol error)
	switch msg := clientMsg.(type) {
	case *pgproto3.SASLInitialResponse:
		log.Printf("[%s] client->backend: SASLInitialResponse (mechanism: %s)", clientAddr, msg.AuthMechanism)
		err = bf.Send(clientMsg)
		if err != nil {
			return fmt.Errorf("failed to send SASL response to backend: %w", err)
		}
	case *pgproto3.PasswordMessage:
		// Client sent wrong message type - this will fail, but forward it anyway
		// so the real error from PostgreSQL reaches the client
		log.Printf("[%s] WARNING: client sent PasswordMessage for SASL auth (protocol mismatch)", clientAddr)
		err = bf.Send(clientMsg)
		if err != nil {
			return fmt.Errorf("failed to send password to backend: %w", err)
		}
	default:
		return fmt.Errorf("unexpected client message for SASL: %T", clientMsg)
	}
	return nil
}

// handleSASLContinue handles SCRAM-SHA-256 authentication continue step
func handleSASLContinue(cb *pgproto3.Backend, bf *pgproto3.Frontend, clientAddr string) error {
	log.Printf("[%s] awaiting SASL response from client", clientAddr)
	clientMsg, err := cb.Receive()
	if err != nil {
		return fmt.Errorf("client disconnected during SASL continue: %w", err)
	}

	switch msg := clientMsg.(type) {
	case *pgproto3.SASLResponse:
		log.Printf("[%s] client->backend: SASLResponse", clientAddr)
		err = bf.Send(clientMsg)
		if err != nil {
			return fmt.Errorf("failed to send SASL response to backend: %w", err)
		}
	default:
		return fmt.Errorf("unexpected client message for SASL continue: %T (expected SASLResponse)", msg)
	}
	return nil
}

// handlePasswordAuth handles MD5 or cleartext password authentication
func handlePasswordAuth(cb *pgproto3.Backend, bf *pgproto3.Frontend, clientAddr string, authMsg pgproto3.BackendMessage) error {
	log.Printf("[%s] awaiting password from client", clientAddr)
	clientMsg, err := cb.Receive()
	if err != nil {
		return fmt.Errorf("client disconnected during password auth: %w", err)
	}

	switch msg := clientMsg.(type) {
	case *pgproto3.PasswordMessage:
		log.Printf("[%s] client->backend: PasswordMessage", clientAddr)
		err = bf.Send(clientMsg)
		if err != nil {
			return fmt.Errorf("failed to send password to backend: %w", err)
		}
	default:
		return fmt.Errorf("unexpected client message for password auth: %T (expected PasswordMessage)", msg)
	}
	return nil
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
	// No Flush on Backend; Send writes directly
	return fmt.Errorf("%s", msg)
}
