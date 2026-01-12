package logging

import (
	"log"
	"strings"
)

// Level represents the logging level
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var currentLevel = LevelInfo

// SetLevel sets the global logging level from a string
func SetLevel(level string) {
	switch strings.ToLower(level) {
	case "debug":
		currentLevel = LevelDebug
	case "info":
		currentLevel = LevelInfo
	case "warn", "warning":
		currentLevel = LevelWarn
	case "error":
		currentLevel = LevelError
	default:
		currentLevel = LevelInfo
	}
	log.Printf("Log level set to: %s", strings.ToLower(level))
}

// Debug logs a message at DEBUG level
func Debug(format string, v ...interface{}) {
	if currentLevel <= LevelDebug {
		log.Printf(format, v...)
	}
}

// Info logs a message at INFO level
func Info(format string, v ...interface{}) {
	if currentLevel <= LevelInfo {
		log.Printf(format, v...)
	}
}

// Warn logs a message at WARN level
func Warn(format string, v ...interface{}) {
	if currentLevel <= LevelWarn {
		log.Printf(format, v...)
	}
}

// Error logs a message at ERROR level
func Error(format string, v ...interface{}) {
	if currentLevel <= LevelError {
		log.Printf(format, v...)
	}
}
