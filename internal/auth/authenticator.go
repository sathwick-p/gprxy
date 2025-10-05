package auth

import (
	"crypto/md5"
	"fmt"
	"io"
	"log"
	"net"
	"time"

	"github.com/jackc/pgproto3/v2"
	"github.com/xdg-go/scram"
)

// AuthenticateUser authenticates a user with PostgreSQL using a temporary connection
// The proxy acts as a PostgreSQL client and handles all authentication methods (SCRAM, MD5, etc.)
func AuthenticateUser(user, database, host string, startUpMessage *pgproto3.StartupMessage, clientBackend *pgproto3.Backend, clientAddr string) (pgproto3.BackendKeyData, error) {
	backendAddress := host + ":5432"
	log.Printf("[%s] connecting to PostgreSQL at %s as %s for authentication", clientAddr, backendAddress, user)

	tempConnection, err := net.DialTimeout("tcp", backendAddress, 10*time.Second)
	if err != nil {
		log.Printf("[%s] failed to connect to PostgreSQL: %v", clientAddr, err)
		return pgproto3.BackendKeyData{}, sendErrorToClient(clientBackend, "Backend Unavailable")
	}
	defer tempConnection.Close()

	tempFrontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(tempConnection), tempConnection)
	log.Printf("[%s] sending startup message to PostgreSQL", clientAddr)
	err = tempFrontend.Send(startUpMessage)
	if err != nil {
		log.Printf("[%s] failed to send startup message: %v", clientAddr, err)
		return pgproto3.BackendKeyData{}, sendErrorToClient(clientBackend, "Authentication failed")
	}

	// First, ask the client for their password
	log.Printf("[%s] requesting password from client", clientAddr)
	password, err := requestPasswordFromClient(clientBackend, clientAddr)
	if err != nil {
		log.Printf("[%s] failed to get password from client: %v", clientAddr, err)
		return pgproto3.BackendKeyData{}, sendErrorToClient(clientBackend, "Authentication failed")
	}

	// Now authenticate WITH PostgreSQL using the password
	log.Printf("[%s] starting authentication with PostgreSQL backend", clientAddr)
	var backendKeyData *pgproto3.BackendKeyData
	err = authenticateWithBackend(tempFrontend, clientBackend, user, password, clientAddr, &backendKeyData)
	if err != nil {
		log.Printf("[%s] authentication with backend failed: %v", clientAddr, err)
		return pgproto3.BackendKeyData{}, err
	}

	log.Printf("[%s] authentication completed successfully", clientAddr)
	return *backendKeyData, nil
}

// requestPasswordFromClient asks the client for their password
// We send an AuthenticationCleartextPassword request to the client
func requestPasswordFromClient(clientBackend *pgproto3.Backend, clientAddr string) (string, error) {
	// Ask client for cleartext password
	err := clientBackend.Send(&pgproto3.AuthenticationCleartextPassword{})
	if err != nil {
		return "", fmt.Errorf("failed to request password: %w", err)
	}

	// Receive password from client
	msg, err := clientBackend.Receive()
	if err != nil {
		return "", fmt.Errorf("failed to receive password: %w", err)
	}

	passwordMsg, ok := msg.(*pgproto3.PasswordMessage)
	if !ok {
		return "", fmt.Errorf("expected PasswordMessage, got %T", msg)
	}
	return passwordMsg.Password, nil
}

// authenticateWithBackend performs authentication WITH the PostgreSQL backend
// The proxy acts as a PostgreSQL client and handles SCRAM, MD5, etc.
func authenticateWithBackend(frontend *pgproto3.Frontend, clientBackend *pgproto3.Backend, username, password, clientAddr string, backendKeyData **pgproto3.BackendKeyData) error {
	var scramConversation *scram.ClientConversation

	for {
		msg, err := frontend.Receive()
		if err != nil {
			return fmt.Errorf("failed to receive from backend: %w", err)
		}

		log.Printf("[%s] backend auth message: %T", clientAddr, msg)

		switch authMsg := msg.(type) {
		case *pgproto3.ErrorResponse:
			log.Printf("[%s] backend auth error: %s - %s", clientAddr, authMsg.Code, authMsg.Message)
			// Forward error to client
			clientBackend.Send(authMsg)
			return fmt.Errorf("backend auth error: %s", authMsg.Message)

		case *pgproto3.AuthenticationOk:
			log.Printf("[%s] backend authentication OK", clientAddr)
			// Send AuthenticationOk to client
			err := clientBackend.Send(&pgproto3.AuthenticationOk{})
			if err != nil {
				return fmt.Errorf("failed to send AuthenticationOk to client: %w", err)
			}
			continue

		case *pgproto3.ReadyForQuery:
			log.Printf("[%s] temp auth connection ready for queries (will NOT forward ReadyForQuery yet)", clientAddr)
			// DO NOT send ReadyForQuery to client yet
			// The client will receive ReadyForQuery after the pool connection's BackendKeyData is sent
			return nil

		case *pgproto3.ParameterStatus:
			log.Printf("[%s] parameter status: %s = %s", clientAddr, authMsg.Name, authMsg.Value)

			// Forward to client
			err := clientBackend.Send(authMsg)
			if err != nil {
				return fmt.Errorf("failed to forward ParameterStatus: %w", err)
			}
			continue

		case *pgproto3.BackendKeyData:
			log.Printf("[%s] temp auth connection backend key data received (will NOT forward), pid=%v, secret_key=%v", clientAddr, authMsg.ProcessID, authMsg.SecretKey)
			// Store the BackendKeyData but DO NOT forward to client
			// The client will receive the pool connection's BackendKeyData later
			*backendKeyData = authMsg
			continue

		case *pgproto3.AuthenticationCleartextPassword:
			log.Printf("[%s] backend requests cleartext password", clientAddr)
			err := frontend.Send(&pgproto3.PasswordMessage{Password: password})
			if err != nil {
				return fmt.Errorf("failed to send cleartext password: %w", err)
			}

		case *pgproto3.AuthenticationMD5Password:
			log.Printf("[%s] backend requests MD5 password", clientAddr)
			// Compute MD5 hash: md5(md5(password + username) + salt)
			h1 := md5.New()
			io.WriteString(h1, password)
			io.WriteString(h1, username)
			h2 := md5.New()
			io.WriteString(h2, fmt.Sprintf("%x", h1.Sum(nil)))
			h2.Write(authMsg.Salt[:])
			passwordHash := fmt.Sprintf("md5%x", h2.Sum(nil))

			err := frontend.Send(&pgproto3.PasswordMessage{Password: passwordHash})
			if err != nil {
				return fmt.Errorf("failed to send MD5 password: %w", err)
			}

		case *pgproto3.AuthenticationSASL:
			log.Printf("[%s] backend requests SASL auth, mechanisms: %v", clientAddr, authMsg.AuthMechanisms)

			// Check if SCRAM-SHA-256 is supported
			scramSupported := false
			for _, mech := range authMsg.AuthMechanisms {
				if mech == "SCRAM-SHA-256" {
					scramSupported = true
					break
				}
			}

			if !scramSupported {
				return fmt.Errorf("SCRAM-SHA-256 not supported by backend, available: %v", authMsg.AuthMechanisms)
			}

			// Create SCRAM client - the proxy acts as the SCRAM client to PostgreSQL
			client, err := scram.SHA256.NewClient(username, password, "")
			if err != nil {
				return fmt.Errorf("failed to create SCRAM client: %w", err)
			}

			scramConversation = client.NewConversation()
			initialResponse, err := scramConversation.Step("")
			if err != nil {
				return fmt.Errorf("SCRAM initial step failed: %w", err)
			}

			log.Printf("[%s] sending SCRAM initial response to backend", clientAddr)
			err = frontend.Send(&pgproto3.SASLInitialResponse{
				AuthMechanism: "SCRAM-SHA-256",
				Data:          []byte(initialResponse),
			})
			if err != nil {
				return fmt.Errorf("failed to send SASL initial response: %w", err)
			}

		case *pgproto3.AuthenticationSASLContinue:
			log.Printf("[%s] backend SASL continue", clientAddr)
			if scramConversation == nil {
				return fmt.Errorf("received SASL continue without conversation")
			}

			response, err := scramConversation.Step(string(authMsg.Data))
			if err != nil {
				return fmt.Errorf("SCRAM continue step failed: %w", err)
			}

			err = frontend.Send(&pgproto3.SASLResponse{
				Data: []byte(response),
			})
			if err != nil {
				return fmt.Errorf("failed to send SASL response: %w", err)
			}

		case *pgproto3.AuthenticationSASLFinal:
			log.Printf("[%s] backend SASL final", clientAddr)
			if scramConversation == nil {
				return fmt.Errorf("received SASL final without conversation")
			}

			_, err := scramConversation.Step(string(authMsg.Data))
			if err != nil {
				return fmt.Errorf("SCRAM final step failed: %w", err)
			}
			// Authentication complete, backend will send AuthenticationOk next

		default:
			log.Printf("[%s] unexpected backend auth message: %T", clientAddr, authMsg)
		}
	}
}
