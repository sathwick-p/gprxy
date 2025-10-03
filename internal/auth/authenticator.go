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
	backendAddress := host + ":5432"
	log.Printf("[%s] connecting to PostgreSQL at %s as %s for authentication", clientAddr, backendAddress, user)

	tempConnection, err := net.DialTimeout("tcp", backendAddress, 10*time.Second)
	if err != nil {
		log.Printf("[%s] failed to connect to PostgreSQL: %v", clientAddr, err)
		return sendErrorToClient(clientBackend, "Backend Unavailable")
	}
	defer tempConnection.Close()

	tempFrontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(tempConnection), tempConnection)

	log.Printf("[%s] sending startup message to PostgreSQL", clientAddr)
	err = tempFrontend.Send(startUpMessage)
	if err != nil {
		log.Printf("[%s] failed to send startup message: %v", clientAddr, err)
		return sendErrorToClient(clientBackend, "Authentication failed")
	}

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
		msg, err := bf.Receive()
		if err != nil {
			return fmt.Errorf("lost connection to backend: %w", err)
		}

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

		case *pgproto3.BackendKeyData:
			log.Printf("[%s] backend key data - process_id: %d, secret_key: %d",
				clientAddr, message.ProcessID, message.SecretKey)

		case *pgproto3.ParameterStatus:
			// Continue to receive more startup messages
		}

		switch authMsg := msg.(type) {
		case *pgproto3.AuthenticationSASL:
			if err := handleSASLAuth(cb, bf, clientAddr); err != nil {
				return err
			}
		case *pgproto3.AuthenticationSASLContinue:
			if err := handleSASLContinue(cb, bf, clientAddr); err != nil {
				return err
			}
		case *pgproto3.AuthenticationSASLFinal:
			log.Printf("[%s] SASL authentication final step", clientAddr)
		case *pgproto3.AuthenticationMD5Password, *pgproto3.AuthenticationCleartextPassword:
			if err := handlePasswordAuth(cb, bf, clientAddr, authMsg); err != nil {
				return err
			}
		}
	}
}
