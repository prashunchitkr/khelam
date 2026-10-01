package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/prashunchitkr/khelamgg/internal/agent"
	"github.com/prashunchitkr/khelamgg/internal/config"
	"github.com/prashunchitkr/khelamgg/internal/logger"
	"github.com/prashunchitkr/khelamgg/internal/version"
)

// exitPermanent is the exit status for failures that restarting cannot
// fix, such as bad configuration or a rejected token. Transient failures
// are retried inside the agent, so any error reaching main is permanent.
// Service managers can use it to stop restarting, e.g. systemd's
// RestartPreventExitStatus.
const exitPermanent = 3

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-version", "--version", "version":
			fmt.Println(version.String())
			return
		}
	}

	logger := logger.New(logger.LoggerOptions{
		Writer: os.Stdout,
		Level:  slog.LevelInfo,
	})

	agentConfig, err := config.GetAgentConfig(os.Args[1:])
	if err != nil {
		logger.Error("error when parsing flags", "error", err)
		os.Exit(exitPermanent)
	}

	agt := agent.New(agent.Options{
		Logger: logger,
		Cfg:    agentConfig,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting khelam agent", "version", version.String(), "agentID", agentConfig.AgentID)

	if err := agt.Run(ctx); err != nil {
		logger.Error("agent stopped", "error", err)
		os.Exit(exitPermanent)
	}

	logger.Info("agent stopped gracefully")
}
