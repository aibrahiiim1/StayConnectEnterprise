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
	// AllowPublicListen (CTRLAPI_ALLOW_PUBLIC_LISTEN=true) lets a production ctrlapi bind the operator
	// listener to every interface. Central puts Caddy in front and binds loopback; this exists only for a
	// deployment that has deliberately put something else in front.
	AllowPublicListen bool
}

// HARDENING IS THE DEFAULT, NOT AN ENV VAR SOMEBODY MUST REMEMBER.
//
// The operator listener used to default to ":8080" (every interface) and the session cookie to Secure=false,
// so a Central whose env file lacked either line served the console API, unencrypted cookies and all, on every
// address of the host. Now the defaults are loopback and Secure, and a PRODUCTION ctrlapi refuses to start
// when either is weakened without the explicit opt-out (Validate).
func Load() (Config, error) {
	c := Config{
		Addr:              env("CTRLAPI_ADDR", "127.0.0.1:8080"),
		DBURL:             env("CTRLAPI_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable"),
		RedisURL:          env("CTRLAPI_REDIS_URL", "redis://127.0.0.1:6379/0"),
		Env:               env("CTRLAPI_ENV", "dev"),
		CookieSecure:      env("CTRLAPI_COOKIE_SECURE", "true") != "false",
		AllowPublicListen: env("CTRLAPI_ALLOW_PUBLIC_LISTEN", "false") == "true",
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

// ListensOnAllInterfaces reports whether addr binds every interface (":8080", "0.0.0.0:8080", "[::]:8080").
func ListensOnAllInterfaces(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return true // unparseable: assume the worst
	}
	if host == "" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

// Validate is the start-time hardening check. It returns the problems that refuse a start (production only)
// and the warnings to log. Called by `ctrlapi serve` before anything listens.
func (c Config) Validate() (errs []string, warnings []string) {
	prod := strings.EqualFold(c.Env, "production") || strings.EqualFold(c.Env, "prod")
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
	return errs, warnings
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
