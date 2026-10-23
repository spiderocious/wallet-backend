// Package env parses configuration once at boot into a typed struct.
// Production-only requirements are checked in cmd/api/main.go, not here, so
// development can boot without production secrets.
package env

import (
	"fmt"

	"github.com/caarlos0/env/v11"
)

// Config is the process configuration. Every field is read from the
// environment exactly once.
type Config struct {
	Env         string `env:"APP_ENV" envDefault:"development"`
	Port        int    `env:"PORT" envDefault:"8080"`
	DatabaseURL string `env:"DATABASE_URL,required"`
	JWTSecret   string `env:"JWT_SECRET,required"`
	CORSOrigin  string `env:"CORS_ORIGIN" envDefault:"http://localhost:5173"`
	DataDir     string `env:"DATA_DIR" envDefault:"./data"`
	// DemoControl mounts /__control, which lets the presenter make the API
	// fail or slow down on demand. Never true in a real deployment.
	DemoControl bool   `env:"DEMO_CONTROL" envDefault:"false"`
	LogLevel    string `env:"LOG_LEVEL" envDefault:"info"`
}

// Load reads the environment. It returns an error rather than exiting so the
// caller decides how to report it.
func Load() (Config, error) {
	var c Config
	if err := env.Parse(&c); err != nil {
		return Config{}, fmt.Errorf("env: %w", err)
	}
	if len(c.JWTSecret) < 32 {
		return Config{}, fmt.Errorf("env: JWT_SECRET must be at least 32 characters")
	}
	return c, nil
}

// IsProduction reports whether the process runs with APP_ENV=production.
func (c Config) IsProduction() bool { return c.Env == "production" }
