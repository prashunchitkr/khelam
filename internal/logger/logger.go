// Package logger implements initialization of logger
package logger

import (
	"io"
	"log/slog"
)

type LoggerOptions struct {
	Writer io.Writer
	Level  slog.Level
}

func New(options LoggerOptions) *slog.Logger {
	handler := slog.NewJSONHandler(options.Writer, &slog.HandlerOptions{
		Level: options.Level,
	})

	logger := slog.New(handler)

	return logger
}
