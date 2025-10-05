package proxy

import (
	"fmt"
	"log"

	"github.com/jackc/pgproto3/v2"
)

// handleMessage handles incoming client messages after authentication
func (pc *Connection) handleMessage(client *pgproto3.Backend) error {
	msg, err := client.Receive()
	if err != nil {
		return fmt.Errorf("client receive error: %w", err)
	}

	clientAddr := pc.conn.RemoteAddr().String()
	key := pc.poolConn.Conn().PgConn().SecretKey()
	pid := pc.poolConn.Conn().PgConn().PID()
	switch query := msg.(type) {
	case *pgproto3.Query:
		log.Printf("[%s] [%s] QUERY: %s", clientAddr, pc.user, query.String)
		log.Printf("[%s] Query connection's pid=%d, secret_key=%d", clientAddr, pid, key)

	case *pgproto3.Parse:
		log.Printf("[%s] [%s] PARSE: statement='%s' query='%s'", clientAddr, pc.user, query.Name, query.Query)

	case *pgproto3.Describe:
		objectType := "statement"
		if query.ObjectType == 'P' {
			objectType = "portal"
		}
		log.Printf("[%s] [%s] DESCRIBE: %s='%s'", clientAddr, pc.user, objectType, query.Name)

	case *pgproto3.Bind:
		paramCount := len(query.Parameters)
		log.Printf("[%s] [%s] BIND: portal='%s' statement='%s' params=%d", clientAddr, pc.user, query.DestinationPortal, query.PreparedStatement, paramCount)

	case *pgproto3.Execute:
		maxRows := "unlimited"
		if query.MaxRows > 0 {
			maxRows = fmt.Sprintf("%d", query.MaxRows)
		}
		log.Printf("[%s] [%s] EXECUTE: portal='%s' max_rows=%s", clientAddr, pc.user, query.Portal, maxRows)

	case *pgproto3.Sync:
		log.Printf("[%s] [%s] SYNC: transaction boundary", clientAddr, pc.user)

	case *pgproto3.Terminate:
		log.Printf("[%s] [%s] TERMINATE: client disconnecting gracefully", clientAddr, pc.user)
		return fmt.Errorf("client terminated")

	default:
		log.Printf("[%s] [%s] UNKNOWN_MESSAGE: %T", clientAddr, pc.user, query)
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
	clientAddr := pc.conn.RemoteAddr().String()

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
			log.Printf("[%s] query completed, ready for next (status: %c)",
				clientAddr, msgType.TxStatus)
			return nil
		case *pgproto3.ErrorResponse:
			log.Printf("[%s] query error: %s (code: %s)",
				clientAddr, msgType.Message, msgType.Code)
		case *pgproto3.CommandComplete:
			log.Printf("[%s] command completed: %s",
				clientAddr, msgType.CommandTag)
		}
	}
}
