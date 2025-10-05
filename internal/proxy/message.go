package proxy

import (
	"fmt"

	"github.com/jackc/pgproto3/v2"

	"gprxy.com/internal/logger"
)

// handleMessage handles incoming client messages after authentication
func (pc *Connection) handleMessage(client *pgproto3.Backend) error {
	msg, err := client.Receive()
	if err != nil {
		return fmt.Errorf("client receive error: %w", err)
	}

	key := pc.poolConn.Conn().PgConn().SecretKey()
	pid := pc.poolConn.Conn().PgConn().PID()
	switch query := msg.(type) {
	case *pgproto3.Query:
		logger.Info("[%s] query: %s", pc.user, query.String)
		logger.Debug("query connection PID=%d, secret_key=%d", pid, key)

	case *pgproto3.Parse:
		logger.Debug("[%s] parse: statement='%s' query='%s'", pc.user, query.Name, query.Query)

	case *pgproto3.Describe:
		objectType := "statement"
		if query.ObjectType == 'P' {
			objectType = "portal"
		}
		logger.Debug("[%s] describe: %s='%s'", pc.user, objectType, query.Name)

	case *pgproto3.Bind:
		paramCount := len(query.Parameters)
		logger.Debug("[%s] bind: portal='%s' statement='%s' params=%d", pc.user, query.DestinationPortal, query.PreparedStatement, paramCount)

	case *pgproto3.Execute:
		maxRows := "unlimited"
		if query.MaxRows > 0 {
			maxRows = fmt.Sprintf("%d", query.MaxRows)
		}
		logger.Debug("[%s] execute: portal='%s' max_rows=%s", pc.user, query.Portal, maxRows)

	case *pgproto3.Sync:
		logger.Debug("[%s] sync: transaction boundary", pc.user)

	case *pgproto3.Terminate:
		logger.Info("[%s] client disconnecting gracefully", pc.user)
		return fmt.Errorf("client terminated")

	default:
		logger.Debug("[%s] unknown message type: %T", pc.user, query)
	}

	err = pc.bf.Send(msg)
	if err != nil {
		return fmt.Errorf("unable to send query to backend: %w", err)
	}

	if _, ok := msg.(*pgproto3.Terminate); ok {
		return fmt.Errorf("connection terminated")
	}

	return pc.relayBackendResponse(client)
}

// relayBackendResponse relays backend responses back to the client
func (pc *Connection) relayBackendResponse(client *pgproto3.Backend) error {
	for {
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
			logger.Debug("query completed, ready for next query (status: %c)",
				msgType.TxStatus)
			return nil
		case *pgproto3.ErrorResponse:
			logger.Warn("query error: %s (code: %s)",
				msgType.Message, msgType.Code)
		case *pgproto3.CommandComplete:
			logger.Debug("command completed: %s",
				msgType.CommandTag)
		}
	}
}
