package auth

import (
	"fmt"

	"github.com/jackc/pgproto3/v2"
)

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
	return fmt.Errorf("%s", msg)
}
