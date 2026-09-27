package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

type Config struct {
	Addr         string
	DBURL        string
	RedisURL     string
	LogLevel     slog.Level
	Env          string
	AllowOrigins []string
	CookieSecure bool
}

func Load() (Config, error) {
	c := Config{
		Addr:         env("CTRLAPI_ADDR", ":8080"),
		DBURL:        env("CTRLAPI_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable"),
		RedisURL:     env("CTRLAPI_REDIS_URL", "redis://127.0.0.1:6379/0"),
		Env:          env("CTRLAPI_ENV", "dev"),
		CookieSecure: env("CTRLAPI_COOKIE_SECURE", "false") == "true",
	}
	level, err := ParseLogLevel(env("CTRLAPI_LOG_LEVEL", "info"))
	if err != nil {
		return c, err
	}
	c.LogLevel = level
	// LOOPBACK ONLY BY DEFAULT. A real deployment sets CTRLAPI_ALLOW_ORIGINS to its own admin origin.
	origins := env("CTRLAPI_ALLOW_ORIGINS", "http://localhost:3000,http://127.0.0.1:3000")
	for _, o := range strings.Split(origins, ",") {
		if s := strings.TrimSpace(o); s != "" {
			c.AllowOrigins = append(c.AllowOrigins, s)
		}
	}
	if c.DBURL == "" {
		return c, fmt.Errorf("CTRLAPI_DB_URL is required")
	}
	return c, nil
}

// ParseLogLevel maps CTRLAPI_LOG_LEVEL (debug, info, warn, error) onto slog.
func ParseLogLevel(s string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug, nil
	case "", "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return slog.LevelInfo, fmt.Errorf("CTRLAPI_LOG_LEVEL %q is not one of debug, info, warn, error", s)
}

func env(k, d string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return d
}
