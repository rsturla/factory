// Package envutil provides helpers for reading environment variables
// with defaults, type conversion, and required-or-exit semantics.
package envutil

import (
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Require returns the value of the environment variable key.
// If the variable is empty or unset, it logs an error and exits.
func Require(key string) string {
	v := os.Getenv(key)
	if v == "" {
		slog.Error("required environment variable not set", "var", key)
		os.Exit(1)
	}
	return v
}

// Or returns the value of the environment variable key,
// or fallback if the variable is empty or unset.
func Or(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Int returns the value of the environment variable key parsed as an int,
// or fallback if the variable is empty, unset, or not a valid integer.
func Int(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

// Duration returns the value of the environment variable key parsed as a
// time.Duration, or fallback if the variable is empty, unset, or not valid.
func Duration(key string, fallback time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return fallback
}
