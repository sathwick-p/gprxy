package auth

import (
	"github.com/jackc/pgproto3/v2"
	"gprxy.com/internal/logger"
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
		return logger.Errorf("failed to send error to client: %w", err)
	}
	return logger.Errorf("%s", msg)
}
