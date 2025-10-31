package auth

import (
	"crypto/md5"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/jackc/pgproto3/v2"
	"github.com/xdg-go/scram"

	"gprxy.com/internal/logger"
)

var (
	jwtValidator *JWTValidator
	roleMapper   *RoleMapper
)

func initializeAuth(issuer, audience, jwksurl string) error {
	jwtValidator = NewJWTValidator(issuer, audience, jwksurl)
	var err error
	roleMapper, err = NewRoleMapper()
	if err != nil {
		logger.Errorf("failed to initalise role mapping")

	}
	logger.Info("authentication initalised (issuer: %s, aud: %s)", issuer, audience)
	logger.Info("configured roles: %v", roleMapper.GetAllRoles())
	return nil
}

// AuthenticateUser authenticates a user with PostgreSQL using a temporary connection
// The proxy acts as a PostgreSQL client and handles all authentication methods (SCRAM, MD5, etc.)
func AuthenticateUser(user, database, host string, startUpMessage *pgproto3.StartupMessage, clientBackend *pgproto3.Backend, clientAddr string) (pgproto3.BackendKeyData, error) {
	backendAddress := net.JoinHostPort(host, "5432")
	logger.Debug("connecting to PostgreSQL at %s as %s for authentication", backendAddress, user)

	tempConnection, err := net.DialTimeout("tcp", backendAddress, 10*time.Second)
	if err != nil {
		logger.Error("failed to connect to PostgreSQL: %v", err)
		return pgproto3.BackendKeyData{}, sendErrorToClient(clientBackend, "Backend Unavailable")
	}
	defer tempConnection.Close()

	// First, ask the client for their password
	password, err := requestPasswordFromClient(clientBackend, clientAddr)
	if err != nil {
		logger.Error("failed to get password from client: %v", err)
		return pgproto3.BackendKeyData{}, sendErrorToClient(clientBackend, "Authentication failed")
	}
	var actualUsername, actualPassword string
	// Checking if it's a JWT token
	if strings.HasPrefix(password, "eyJ") && strings.Count(password, ".") == 2 {
		logger.Debug("jwt token received")

		oauth, err := jwtValidator.ValidateJWT(password)
		if err != nil {
			logger.Errorf("jwt validation failed: %v", err)
			return pgproto3.BackendKeyData{}, sendErrorToClient(clientBackend, "Invalid authentication token")
		}
		svcAcc, err := roleMapper.MapRoleToServiceAccount(oauth.Roles)
		if err != nil {
			logger.Errorf("role mapping failed for user %s: %v", oauth.Email, err)
			return pgproto3.BackendKeyData{}, sendErrorToClient(clientBackend, "Access denied: no valid roles")
		}

		oauth.ServiceAccount = svcAcc.Username
		actualUsername = svcAcc.Username
		actualPassword = svcAcc.Password

		logger.Info("user %s (roles: %v) mapped to service account: %s",
			oauth.Email, oauth.Roles, svcAcc.Username)
	} else {
		// Traditional password authentication (fallback)
		logger.Debug("Traditional password authentication for user: %s", user)
		actualUsername = user
		actualPassword = password
	}
	startUpMessage.Parameters["user"] = actualUsername
	tempFrontend := pgproto3.NewFrontend(pgproto3.NewChunkReader(tempConnection), tempConnection)
	logger.Debug("sending startup message to PostgreSQL")
	err = tempFrontend.Send(startUpMessage)
	if err != nil {
		logger.Error("failed to send startup message: %v", err)
		return pgproto3.BackendKeyData{}, sendErrorToClient(clientBackend, "Authentication failed")
	}

	// Now authenticate WITH PostgreSQL using the password
	var backendKeyData *pgproto3.BackendKeyData
	err = authenticateWithBackend(tempFrontend, clientBackend, user, password, clientAddr, &backendKeyData)
	if err != nil {
		logger.Error("authentication with backend failed: %v", err)
		return pgproto3.BackendKeyData{}, err
	}

	logger.Debug("authentication completed successfully")
	return *backendKeyData, nil
}

// requestPasswordFromClient asks the client for their password
// We send an AuthenticationCleartextPassword request to the client
func requestPasswordFromClient(clientBackend *pgproto3.Backend, clientAddr string) (string, error) {
	// Ask client for cleartext password
	err := clientBackend.Send(&pgproto3.AuthenticationCleartextPassword{})
	if err != nil {
		return "", logger.Errorf("failed to request password: %w", err)
	}

	// Receive password from client
	msg, err := clientBackend.Receive()
	if err != nil {
		return "", logger.Errorf("failed to receive password: %w", err)
	}

	passwordMsg, ok := msg.(*pgproto3.PasswordMessage)
	if !ok {
		return "", logger.Errorf("expected PasswordMessage, got %T", msg)
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
			return logger.Errorf("failed to receive from backend: %w", err)
		}

		logger.Debug("backend auth message: %T", msg)

		switch authMsg := msg.(type) {
		case *pgproto3.ErrorResponse:
			logger.Error("backend auth error: %s - %s", authMsg.Code, authMsg.Message)
			// Forward error to client
			clientBackend.Send(authMsg)
			return logger.Errorf("backend auth error: %s", authMsg.Message)

		case *pgproto3.AuthenticationOk:
			logger.Debug("backend authentication OK")
			// Send AuthenticationOk to client
			err := clientBackend.Send(&pgproto3.AuthenticationOk{})
			if err != nil {
				return logger.Errorf("failed to send AuthenticationOk to client: %w", err)
			}
			continue

		case *pgproto3.ReadyForQuery:
			logger.Debug("temp auth connection ready for queries (will not forward ReadyForQuery yet)")
			// DO NOT send ReadyForQuery to client yet
			// The client will receive ReadyForQuery after the pool connection's BackendKeyData is sent
			return nil

		case *pgproto3.ParameterStatus:
			logger.Debug("parameter status: %s = %s", authMsg.Name, authMsg.Value)

			// Forward to client
			err := clientBackend.Send(authMsg)
			if err != nil {
				return logger.Errorf("failed to forward ParameterStatus: %w", err)
			}
			continue

		case *pgproto3.BackendKeyData:
			logger.Debug("temp auth connection backend key data received (will not forward): PID=%d, secret_key=%d", authMsg.ProcessID, authMsg.SecretKey)
			// Store the BackendKeyData but DO NOT forward to client
			// The client will receive the pool connection's BackendKeyData later
			*backendKeyData = authMsg
			continue

		case *pgproto3.AuthenticationCleartextPassword:
			logger.Debug("backend requests cleartext password")
			err := frontend.Send(&pgproto3.PasswordMessage{Password: password})
			if err != nil {
				return logger.Errorf("failed to send cleartext password: %w", err)
			}

		case *pgproto3.AuthenticationMD5Password:
			logger.Debug("backend requests MD5 password")
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
				return logger.Errorf("failed to send MD5 password: %w", err)
			}

		case *pgproto3.AuthenticationSASL:
			logger.Debug("backend requests SASL auth, mechanisms: %v", authMsg.AuthMechanisms)

			// Check if SCRAM-SHA-256 is supported
			scramSupported := false
			for _, mech := range authMsg.AuthMechanisms {
				if mech == "SCRAM-SHA-256" {
					scramSupported = true
					break
				}
			}

			if !scramSupported {
				return logger.Errorf("SCRAM-SHA-256 not supported by backend, available: %v", authMsg.AuthMechanisms)
			}

			// Create SCRAM client - the proxy acts as the SCRAM client to PostgreSQL
			client, err := scram.SHA256.NewClient(username, password, "")
			if err != nil {
				return logger.Errorf("failed to create SCRAM client: %w", err)
			}

			scramConversation = client.NewConversation()
			initialResponse, err := scramConversation.Step("")
			if err != nil {
				return logger.Errorf("SCRAM initial step failed: %w", err)
			}

			logger.Debug("sending SCRAM initial response to backend")
			err = frontend.Send(&pgproto3.SASLInitialResponse{
				AuthMechanism: "SCRAM-SHA-256",
				Data:          []byte(initialResponse),
			})
			if err != nil {
				return logger.Errorf("failed to send SASL initial response: %w", err)
			}

		case *pgproto3.AuthenticationSASLContinue:
			logger.Debug("backend SASL continue")
			if scramConversation == nil {
				return logger.Errorf("received SASL continue without conversation")
			}

			response, err := scramConversation.Step(string(authMsg.Data))
			if err != nil {
				return logger.Errorf("SCRAM continue step failed: %w", err)
			}

			err = frontend.Send(&pgproto3.SASLResponse{
				Data: []byte(response),
			})
			if err != nil {
				return logger.Errorf("failed to send SASL response: %w", err)
			}

		case *pgproto3.AuthenticationSASLFinal:
			logger.Debug("backend SASL final")
			if scramConversation == nil {
				return logger.Errorf("received SASL final without conversation")
			}

			_, err := scramConversation.Step(string(authMsg.Data))
			if err != nil {
				return logger.Errorf("SCRAM final step failed: %w", err)
			}
			// Authentication complete, backend will send AuthenticationOk next

		default:
			logger.Warn("unexpected backend auth message: %T", authMsg)
		}
	}
}
