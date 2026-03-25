// Package config defines the pve-imds configuration structure and defaults.
package config

// Config holds all pve-imds configuration.
type Config struct {
	// LogLevel is the minimum log level to emit (debug, info, warn, error).
	LogLevel string `mapstructure:"log_level"`
}

// Default returns a Config with sensible defaults.
func Default() Config {
	return Config{
		LogLevel: "info",
	}
}
