package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/logger"
	"github.com/prashunchitkr/khelamgg/internal/server"
)

func main() {
	logger := logger.New(logger.LoggerOptions{
		Writer: os.Stdout,
		Level:  slog.LevelInfo,
	})

	serverConfig, err := config.GetServerConfig(os.Args[1:])
	if err != nil {
		logger.Error("error when parsing flags", "error", err)
		os.Exit(1)
	}

	srv := server.New(server.Options{
		Logger: logger,
		Cfg:    serverConfig,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := srv.Start(ctx); err != nil {
		logger.Error("error starting server", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped gracefully")
}
