package config

import (
	"fmt"
	"log/slog"
	"net"
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
	// AllowPublicListen lets a production ctrlapi bind the operator listener to every interface; Central binds
	// loopback behind Caddy, so this is only for a deployment that deliberately fronts it otherwise.
	AllowPublicListen bool
	// DBURLDefaulted: CTRLAPI_DB_URL was not set and the development default (a published password, TLS off)
	// is in use. A production ctrlapi refuses to start on it (audit Phase 10 X3).
	DBURLDefaulted bool
}

// devDBURL is the development database; never acceptable in production.
const devDBURL = "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable"

// Hardening is the default: loopback listener and Secure cookie, and a production ctrlapi refuses to start
// when either is weakened without the explicit opt-out (Validate).
func Load() (Config, error) {
	c := Config{
		Addr:              env("CTRLAPI_ADDR", "127.0.0.1:8080"),
		DBURL:             env("CTRLAPI_DB_URL", devDBURL),
		RedisURL:          env("CTRLAPI_REDIS_URL", "redis://127.0.0.1:6379/0"),
		Env:               env("CTRLAPI_ENV", "dev"),
		CookieSecure:      env("CTRLAPI_COOKIE_SECURE", "true") != "false",
		AllowPublicListen: env("CTRLAPI_ALLOW_PUBLIC_LISTEN", "false") == "true",
	}
	c.DBURLDefaulted = env("CTRLAPI_DB_URL", "") == ""
	level, err := ParseLogLevel(env("CTRLAPI_LOG_LEVEL", "info"))
	if err != nil {
		return c, err
	}
	c.LogLevel = level
	// Loopback only by default; a real deployment sets CTRLAPI_ALLOW_ORIGINS to its admin origin.
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

func ListensOnAllInterfaces(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true // unparseable: assume every interface
	}
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// Production reports CTRLAPI_ENV=production (or prod).
func (c Config) Production() bool {
	return strings.EqualFold(c.Env, "production") || strings.EqualFold(c.Env, "prod")
}

// Validate returns the problems that refuse a start (production only) and the warnings to log.
func (c Config) Validate() (errs []string, warnings []string) {
	prod := c.Production()
	report := func(msg string) {
		if prod {
			errs = append(errs, msg)
		} else {
			warnings = append(warnings, msg)
		}
	}
	if ListensOnAllInterfaces(c.Addr) && !c.AllowPublicListen {
		report(fmt.Sprintf("CTRLAPI_ADDR=%q binds every interface; the operator API belongs on loopback behind Caddy "+
			"(127.0.0.1:8080). Set CTRLAPI_ALLOW_PUBLIC_LISTEN=true only if something else deliberately fronts it", c.Addr))
	}
	if !c.CookieSecure {
		report("CTRLAPI_COOKIE_SECURE=false: the console session cookie would be sent over plain HTTP")
	}
	if c.DBURLDefaulted || c.DBURL == devDBURL {
		report("CTRLAPI_DB_URL is not set: ctrlapi would connect with the published development password; set it in the env file")
	}
	return errs, warnings
}

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
