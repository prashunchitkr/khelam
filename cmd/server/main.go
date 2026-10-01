package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/prashunchitkr/khelamgg/internal/auth"
	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/logger"
	"github.com/prashunchitkr/khelamgg/internal/server"
	"github.com/prashunchitkr/khelamgg/internal/version"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-version", "--version", "version":
			fmt.Println(version.String())
			return
		case "gen-token":
			os.Exit(genToken(os.Args[2:]))
		}
	}

	logger := logger.New(logger.LoggerOptions{
		Writer: os.Stdout,
		Level:  slog.LevelInfo,
	})

	serverConfig, err := config.GetServerConfig(os.Args[1:])
	if err != nil {
		logger.Error("error when parsing flags", "error", err)
		os.Exit(1)
	}

	srv, err := server.New(server.Options{
		Logger: logger,
		Cfg:    serverConfig,
	})
	if err != nil {
		logger.Error("error initializing server", "error", err)
		os.Exit(1)
	}

	// SIGHUP reloads the tokens file without dropping other agents.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	go func() {
		for range hup {
			if err := srv.ReloadTokens(); err != nil {
				logger.Error("failed to reload tokens", "error", err)
			}
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting khelam server", "version", version.String())

	if err := srv.Start(ctx); err != nil {
		logger.Error("error starting server", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped gracefully")
}

// genToken prints a new agent token and the tokens file entry for it.
func genToken(args []string) int {
	if len(args) != 1 {
		fmt.Fprintln(os.Stderr, "usage: khelam-server gen-token NAME")
		return 2
	}

	token, entry, err := auth.Generate(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}

	fmt.Printf(`Token for %q (give it to the agent; it is not stored anywhere):

  %s

Add this entry under "tokens:" in the server's tokens file, then send
the server SIGHUP to load it:

  - name: %s
    sha256: %s
`, entry.Name, token, entry.Name, entry.SHA256)

	return 0
}
