package pool

import (
	"log"
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
		log.Printf("No pool found for user %s and database: %s", user, database)
		return
	}

	stats := pool.Stat()
	log.Printf("Pool stats for [%s,%s]- Total: %d, Acquired: %d, Idle: %d", user, database,
		stats.TotalConns(), stats.AcquiredConns(), stats.IdleConns())
}
