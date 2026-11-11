package hc

import (
	"log/slog"
	"time"
)

// Config controls a Hello Center process. Zero values for rates and limits
// are replaced with production defaults. Negative rate limits disable that limiter.
type Config struct {
	PublicBaseURL      string
	RedisAddr          string
	RedisPassword      string
	RedisDB            int
	KeyPrefix          string
	AssetDir           string
	WebDir             string
	AdminUsername      string
	AdminPassword      string
	BcryptCost         int
	MaxUploadBytes     int64
	JoinRatePerMinute  int
	LoginRatePerMinute int
	WriteRatePerMinute int
	CookieSecure       bool
	SessionTTL         time.Duration
	SSEPollInterval    time.Duration
	Logger             *slog.Logger
}

func (c Config) normalized() Config {
	if c.BcryptCost == 0 {
		c.BcryptCost = 12
	}
	if c.MaxUploadBytes == 0 {
		c.MaxUploadBytes = 10 << 20
	}
	if c.JoinRatePerMinute == 0 {
		c.JoinRatePerMinute = 30
	}
	if c.LoginRatePerMinute == 0 {
		c.LoginRatePerMinute = 10
	}
	if c.WriteRatePerMinute == 0 {
		c.WriteRatePerMinute = 180
	}
	if c.SessionTTL == 0 {
		c.SessionTTL = 7 * 24 * time.Hour
	}
	if c.SSEPollInterval == 0 {
		c.SSEPollInterval = time.Second
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.AdminUsername == "" {
		c.AdminUsername = "admin"
	}
	if c.RedisAddr == "" {
		c.RedisAddr = "127.0.0.1:6379"
	}
	if c.KeyPrefix == "" {
		c.KeyPrefix = "hc"
	}
	return c
}
