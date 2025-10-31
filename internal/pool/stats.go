package pool

import (
	"gprxy/internal/logger"
)

// LogPoolStats logs statistics for the given database pool
func LogPoolStats(user, database string) {
	key := poolKey{
		user:     user,
		database: database,
	}
	poolMutex.RLock()
	pool, exists := poolManager[key]
	poolMutex.RUnlock()

	if !exists {
		logger.Warn("no pool found for user %s and database: %s", user, database)
		return
	}

	stats := pool.Stat()
	logger.Debug("pool stats for [%s,%s] - total: %d, acquired: %d, idle: %d", user, database,
		stats.TotalConns(), stats.AcquiredConns(), stats.IdleConns())
}
