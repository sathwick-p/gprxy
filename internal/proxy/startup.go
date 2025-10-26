package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"time"

	"github.com/jackc/pgproto3/v2"

	"gprxy.com/internal/auth"
	"gprxy.com/internal/logger"
)

// handleStartupMessage handles the initial client startup message
func (pc *Connection) handleStartupMessage(pgconn *pgproto3.Backend) (*pgproto3.Backend, error) {
	clientAddr := pc.conn.RemoteAddr().String()

	startupMessage, err := pgconn.ReceiveStartupMessage()
	if err != nil {
		return nil, logger.Errorf("error receiving startup message: %w", err)
	}

	logger.Debug("received startup message type: %T", startupMessage)

	switch msg := startupMessage.(type) {
	case *pgproto3.StartupMessage:
		user := msg.Parameters["user"]
		database := msg.Parameters["database"]
		appName := msg.Parameters["application_name"]
		logger.Info("connection request - user: %s, database: %s, app: %s",
			user, database, appName)

		keyData, err := auth.AuthenticateUser(user, database, pc.config.Host, msg, pgconn, clientAddr)
		if err != nil {
			return nil, err
		}
		logger.Info("user %s authenticated successfully", user)
		pc.key = &keyData
		start := time.Now()
		err = pc.connectBackend(database, user)
		if err != nil {
			logger.Error("failed to connect to backend: %v", err)
			return nil, pc.sendErrorToClient(pgconn, "Database unavailable")
		}
		_, err = pc.poolConn.Exec(context.Background(), fmt.Sprintf("SET ROLE %s", user))
		if err != nil {
			pc.poolConn.Conn().Close(context.Background())
			return nil, pc.sendErrorToClient(pgconn, "failed to assume user role")

		}
		logger.Debug("backend connection established in %v", time.Since(start))
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

		logger.Debug("pool connection backend key: PID=%d, secret_key=%d", backendPID, backendSecretKey)
		err = pgconn.Send(pc.key)
		if err != nil {
			logger.Error("failed to send BackendKeyData to client: %v", err)
			return nil, logger.Errorf("failed to send backend key data")
		}
		logger.Debug("sent pool BackendKeyData to client")

		// Now send ReadyForQuery to complete the startup sequence
		readyMsg := &pgproto3.ReadyForQuery{TxStatus: 'I'} // 'I' = idle
		err = pgconn.Send(readyMsg)
		if err != nil {
			logger.Error("failed to send ReadyForQuery to client: %v", err)
			return nil, logger.Errorf("failed to send ready for query")
		}
		logger.Debug("sent ReadyForQuery to client")

		if pc.key != nil && pc.server != nil {
			pc.server.registerConnection(pc.key.ProcessID, pc.key.SecretKey, pc)
			logger.Debug("registered connection: PID=%d, secret_key=%d",
				pc.key.ProcessID, pc.key.SecretKey)
		}
	case *pgproto3.SSLRequest:
		logger.Debug("SSL request received")

		if pc.tlsConfig == nil {
			logger.Debug("SSL not configured, rejecting request")
			_, err := pc.conn.Write([]byte{'N'})
			if err != nil {
				return nil, logger.Errorf("failed to send SSL rejection: %w", err)
			}
			return pc.handleStartupMessage(pgconn)
		}

		logger.Debug("SSL configured, upgrading connection to TLS")
		_, err := pc.conn.Write([]byte{'S'})
		if err != nil {
			return nil, logger.Errorf("failed to send SSL acceptance: %w", err)
		}

		tlsConn := tls.Server(pc.conn, pc.tlsConfig)
		err = tlsConn.Handshake()
		if err != nil {
			return nil, logger.Errorf("TLS handshake failed: %w", err)
		}

		logger.Debug("TLS handshake completed successfully")

		pc.conn = tlsConn

		pgconn = pgproto3.NewBackend(pgproto3.NewChunkReader(tlsConn), tlsConn)

		return pc.handleStartupMessage(pgconn)

	case *pgproto3.CancelRequest:
		logger.Info("cancel request received: PID=%d, secret_key=%d", msg.ProcessID, msg.SecretKey)
		targetConn, exists := pc.server.getConnectionForCancelRequest(msg.ProcessID, msg.SecretKey)
		if !exists {
			logger.Warn("cancel request for unknown connection")
			return nil, logger.Errorf("cancel request processed - connection unknown")
		}

		logger.Debug("found target connection: user=%s, db=%s", targetConn.user, targetConn.db)

		err := cancelRequest(pc.config.Host, msg)
		if err != nil {
			logger.Error("failed to forward cancel request: %v", err)
			return nil, logger.Errorf("cancel request failed: %w", err)
		}
		logger.Info("cancel request forwarded successfully")
		return nil, logger.Errorf("cancel request processed")
	}

	return pgconn, nil
}
