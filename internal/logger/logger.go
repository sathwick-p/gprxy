package logger

import (
	"fmt"
	"log"
	"os"

	"github.com/joho/godotenv"
)

// LogLevel represents the logging level
type LogLevel int

const (
	// DEBUG level for detailed debugging information
	DEBUG LogLevel = iota
	// INFO level for general informational messages
	INFO
)

var currentLevel = INFO

// SetLevel sets the current logging level
func SetLevel(level LogLevel) {
	currentLevel = level
}

// SetDebug enables debug logging
func SetDebug() {
	currentLevel = DEBUG
	log.Println("Debug logging enabled")
}

// SetProduction enables production logging (INFO only)
func SetProduction() {
	currentLevel = INFO
}

// IsDebug returns true if debug logging is enabled
func IsDebug() bool {
	return currentLevel == DEBUG
}

// Debug logs a message at DEBUG level
func Debug(format string, args ...interface{}) {
	if currentLevel <= DEBUG {
		log.Printf("[DEBUG] "+format, args...)
	}
}

// Info logs a message at INFO level
func Info(format string, args ...interface{}) {
	if currentLevel <= INFO {
		log.Printf("[INFO] "+format, args...)
	}
}

// Error logs an error message (always shown)
func Error(format string, args ...interface{}) {
	log.Printf("[ERROR] "+format, args...)
}

// Errorf logs an error message and returns it as an error type
func Errorf(format string, args ...interface{}) error {
	log.Printf("[ERROR] "+format, args...)
	return fmt.Errorf(format, args...)
}

// Warn logs a warning message (always shown)
func Warn(format string, args ...interface{}) {
	log.Printf("[WARN] "+format, args...)
}

// Warnf logs a warning message and returns it as an error type
func Warnf(format string, args ...interface{}) error {
	log.Printf("[WARN] "+format, args...)
	return fmt.Errorf(format, args...)
}

func Fatal(format string, args ...interface{}) {
	log.Fatalf("[FATAL] "+format, args...)
}

// InitFromEnv initializes logging based on environment variables
func InitFromEnv() {
	err := godotenv.Load(".env") // filename of your env file
	if err != nil {
		log.Printf("Warning: could not load .env file, falling back to system environment")
	}
	logLevel := os.Getenv("LOG_LEVEL")
	if logLevel == "debug" || logLevel == "DEBUG" {
		SetDebug()
	} else {
		SetProduction()
	}
}

// Printf is a convenience method that logs at INFO level
func Printf(format string, args ...interface{}) {
	Info(format, args...)
}

// Sprintf formats a log message without printing it
func Sprintf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}
