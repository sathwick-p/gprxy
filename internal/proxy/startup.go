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

		err := auth.AuthenticateUser(user, database, pc.config.Host, msg, pgconn, clientAddr)
		if err != nil {
			return nil, err
		}
		log.Printf("[%s] user %s authenticated successfully", clientAddr, user)

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
		log.Printf("[%s] cancel request received (not implemented)", clientAddr)
		return nil, fmt.Errorf("cancel requests not implemented")
	}

	return pgconn, nil
}
