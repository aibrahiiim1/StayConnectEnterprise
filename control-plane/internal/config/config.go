package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	Addr         string
	DBURL        string
	RedisURL     string
	NATSURL      string
	LogLevel     string
	Env          string
	AllowOrigins []string
	CookieSecure bool
}

func Load() (Config, error) {
	c := Config{
		Addr:         env("CTRLAPI_ADDR", ":8080"),
		DBURL:        env("CTRLAPI_DB_URL", "postgres://stayconnect:stayconnect@127.0.0.1:5432/stayconnect?sslmode=disable"),
		RedisURL:     env("CTRLAPI_REDIS_URL", "redis://127.0.0.1:6379/0"),
		NATSURL:      env("CTRLAPI_NATS_URL", "nats://127.0.0.1:4222"),
		LogLevel:     strings.ToLower(env("CTRLAPI_LOG_LEVEL", "info")),
		Env:          env("CTRLAPI_ENV", "dev"),
		CookieSecure: env("CTRLAPI_COOKIE_SECURE", "false") == "true",
	}
	// LOOPBACK ONLY BY DEFAULT. This used to also allow http://172.21.60.23:3000 — one particular
	// appliance's address, from one particular lab, compiled into the product. A real deployment sets
	// CTRLAPI_ALLOW_ORIGINS to its own admin origin; a default naming somebody's old machine is either
	// dead weight or, if that address is ever reused, a browser origin nobody authorised.
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

func env(k, d string) string {
	if v, ok := os.LookupEnv(k); ok && v != "" {
		return v
	}
	return d
}
