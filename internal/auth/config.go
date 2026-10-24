package auth

import (
	"os"
	"time"
)

type Config struct {
	IdleTimeout     time.Duration
	AbsoluteTimeout time.Duration
	RotateInterval  time.Duration
	GracePeriod     time.Duration
	CookieName      string
	SecureCookie    bool
}

func DefaultConfig() Config {
	return Config{
		IdleTimeout:     30 * time.Minute,
		AbsoluteTimeout: 8 * time.Hour,
		RotateInterval:  15 * time.Minute,
		GracePeriod:     30 * time.Second,
		CookieName:      "session_id",
		SecureCookie:    os.Getenv("APP_ENV") == "production",
	}
}
