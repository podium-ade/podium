package server

import (
	"errors"
	"fmt"
	"os"

	"github.com/alvaroibarguen/podium/internal/transport/dev"
)

// The transports PODIUM_TRANSPORT accepts. tailnet arrives in step 11.
const (
	TransportDev     = "dev"
	TransportTailnet = "tailnet"
)

// Config is the whole of podium-server's configuration. The names are the canonical ones.
type Config struct {
	// DatabaseURL is PODIUM_DATABASE_URL. Required.
	DatabaseURL string
	// Transport is PODIUM_TRANSPORT: dev (default) or tailnet.
	Transport string
	// DevListen is PODIUM_DEV_LISTEN, default 127.0.0.1:8080. It must be loopback.
	DevListen string
	// DevToken is PODIUM_DEV_TOKEN, the shared bearer token of the dev transport.
	DevToken string
}

// ConfigFromEnv reads the canonical environment variables and applies the defaults.
func ConfigFromEnv() Config {
	return Config{
		DatabaseURL: os.Getenv("PODIUM_DATABASE_URL"),
		Transport:   envOr("PODIUM_TRANSPORT", TransportDev),
		DevListen:   envOr("PODIUM_DEV_LISTEN", dev.DefaultListen),
		DevToken:    os.Getenv("PODIUM_DEV_TOKEN"),
	}
}

// Validate reports the first thing that would stop the server from starting.
func (c Config) Validate() error {
	if c.DatabaseURL == "" {
		return errors.New("PODIUM_DATABASE_URL is required")
	}
	switch c.Transport {
	case TransportDev:
		if c.DevToken == "" {
			return errors.New("PODIUM_DEV_TOKEN is required for PODIUM_TRANSPORT=dev")
		}
	case TransportTailnet:
		return errors.New("PODIUM_TRANSPORT=tailnet is not implemented yet (step 11); use dev")
	default:
		return fmt.Errorf("PODIUM_TRANSPORT=%q is not a transport (want %s or %s)",
			c.Transport, TransportDev, TransportTailnet)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
