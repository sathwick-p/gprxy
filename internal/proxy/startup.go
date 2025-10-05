package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"time"

	"github.com/jackc/pgproto3/v2"

	"gprxy.com/internal/auth"
)

// handleStartupMessage handles the initial client startup message
func (pc *Connection) handleStartupMessage(pgconn *pgproto3.Backend) (*pgproto3.Backend, error) {
	clientAddr := pc.conn.RemoteAddr().String()

	startupMessage, err := pgconn.ReceiveStartupMessage()
	if err != nil {
		return nil, fmt.Errorf("error receiving startup message: %w", err)
	}

	log.Printf("[%s] received startup message type: %T", clientAddr, startupMessage)

	switch msg := startupMessage.(type) {
	case *pgproto3.StartupMessage:
		user := msg.Parameters["user"]
		database := msg.Parameters["database"]
		appName := msg.Parameters["application_name"]
		log.Printf("[%s] connection request - user: %s, database: %s, app: %s",
			clientAddr, user, database, appName)

		keyData, err := auth.AuthenticateUser(user, database, pc.config.Host, msg, pgconn, clientAddr)
		if err != nil {
			return nil, err
		}
		log.Printf("[%s] user %s authenticated successfully", clientAddr, user)
		pc.key = &keyData
		start := time.Now()
		err = pc.connectBackend(database, user)
		if err != nil {
			log.Printf("[%s] failed to connect to backend: %v", clientAddr, err)
			return nil, pc.sendErrorToClient(pgconn, "Database unavailable")
		}
		_, err = pc.poolConn.Exec(context.Background(), fmt.Sprintf("SET ROLE %s", user))
		if err != nil {
			pc.poolConn.Conn().Close(context.Background())
			return nil, pc.sendErrorToClient(pgconn, "failed to assume user role")

		}
		log.Printf("[%s] backend connection established in %v", clientAddr, time.Since(start))
		underlyingConn := pc.poolConn.Conn().PgConn().Conn()

		bf := pgproto3.NewFrontend(pgproto3.NewChunkReader(underlyingConn), underlyingConn)
		pc.bf = bf
		pc.user = user
		pc.db = database

		backendPID := pc.poolConn.Conn().PgConn().PID()
		backendSecretKey := pc.poolConn.Conn().PgConn().SecretKey()

		pc.key = &pgproto3.BackendKeyData{
			ProcessID: uint32(backendPID),
			SecretKey: uint32(backendSecretKey),
		}

		log.Printf("[%s] Pool connection backend key: PID=%d, Key=%d", clientAddr, backendPID, backendSecretKey)
		err = pgconn.Send(pc.key)
		if err != nil {
			log.Printf("[%s] failed to send BackendKeyData to client: %v", clientAddr, err)
			return nil, fmt.Errorf("failed to send backend key data")
		}
		log.Printf("[%s] Sent pool BackendKeyData to client", clientAddr)

		if pc.key != nil && pc.server != nil {
			pc.server.registerConnection(pc.key.ProcessID, pc.key.SecretKey, pc)
			log.Printf("[%s] Registered connection: PID=%d, Key=%d",
				clientAddr, pc.key.ProcessID, pc.key.SecretKey)
		}
	case *pgproto3.SSLRequest:
		log.Printf("[%s] SSL request received", clientAddr)

		if pc.tlsConfig == nil {
			log.Printf("[%s] SSL not configured, rejecting request", clientAddr)
			_, err := pc.conn.Write([]byte{'N'})
			if err != nil {
				return nil, fmt.Errorf("failed to send SSL rejection: %w", err)
			}
			return pc.handleStartupMessage(pgconn)
		}

		log.Printf("[%s] SSL configured, upgrading connection to TLS", clientAddr)
		_, err := pc.conn.Write([]byte{'S'})
		if err != nil {
			return nil, fmt.Errorf("failed to send SSL acceptance: %w", err)
		}

		tlsConn := tls.Server(pc.conn, pc.tlsConfig)
		err = tlsConn.Handshake()
		if err != nil {
			return nil, fmt.Errorf("TLS handshake failed: %w", err)
		}

		log.Printf("[%s] TLS handshake completed successfully", clientAddr)

		pc.conn = tlsConn

		pgconn = pgproto3.NewBackend(pgproto3.NewChunkReader(tlsConn), tlsConn)

		return pc.handleStartupMessage(pgconn)

	case *pgproto3.CancelRequest:
		log.Printf("[%s] cancel request received for pid=%v, secret_key=%d", clientAddr, msg.ProcessID, msg.SecretKey)
		targetConn, exists := pc.server.getConnectionForCancelRequest(msg.ProcessID, msg.SecretKey)
		if !exists {
			log.Printf("[%s] cancel request for unknown connection", clientAddr)
			return nil, fmt.Errorf("cancel request processed - connection unknown")
		}

		log.Printf("[%s] found target connection: user=%s, db=%s", clientAddr, targetConn.user, targetConn.db)

		err := cancelRequest(pc.config.Host, msg)
		if err != nil {
			log.Printf("[%s] failed to forward cancel: %v", clientAddr, err)
			return nil, fmt.Errorf("cancel request failed: %w", err)
		}
		log.Printf("[%s] cancel request processed successfully", clientAddr)
		return nil, fmt.Errorf("cancel requests processed")
	}

	return pgconn, nil
}
