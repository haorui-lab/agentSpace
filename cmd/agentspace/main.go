package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/haorui-lab/agentSpace/internal/as"
)

func main() {
	loadDotEnv(".env")
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	dataDir := env("DATA_DIR", "data")
	cfg := as.Config{
		PublicBaseURL:      strings.TrimRight(os.Getenv("PUBLIC_BASE_URL"), "/"),
		RedisAddr:          env("REDIS_ADDR", "127.0.0.1:6379"),
		RedisPassword:      os.Getenv("REDIS_PASSWORD"),
		RedisDB:            envInt("REDIS_DB", 0),
		KeyPrefix:          env("REDIS_PREFIX", "as"),
		AssetDir:           env("ASSET_DIR", filepath.Join(dataDir, "assets")),
		WebDir:             env("WEB_DIR", "web/dist"),
		AdminUsername:      env("ADMIN_USERNAME", "admin"),
		AdminPassword:      os.Getenv("ADMIN_PASSWORD"),
		BcryptCost:         envInt("BCRYPT_COST", 0),
		MaxUploadBytes:     int64(envInt("MAX_UPLOAD_BYTES", 0)),
		JoinRatePerMinute:  envInt("JOIN_RATE_PER_MINUTE", 0),
		LoginRatePerMinute: envInt("LOGIN_RATE_PER_MINUTE", 0),
		WriteRatePerMinute: envInt("WRITE_RATE_PER_MINUTE", 0),
		CookieSecure:       strings.HasPrefix(strings.ToLower(os.Getenv("PUBLIC_BASE_URL")), "https://"),
		Logger:             logger,
	}
	if err := os.MkdirAll(cfg.AssetDir, 0o700); err != nil {
		logger.Error("create asset directory", "err", err.Error())
		os.Exit(1)
	}
	app, err := as.Open(cfg)
	if err != nil {
		logger.Error("start", "err", err.Error())
		os.Exit(1)
	}
	defer app.Close()
	addr := env("ADDR", ":8080")
	server := &http.Server{
		Addr:              addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		logger.Info("listening", "addr", addr)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("server", "err", err.Error())
			os.Exit(1)
		}
	}()
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func envInt(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func loadDotEnv(path string) {
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		value = strings.Trim(value, `"'`)
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
