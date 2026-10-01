package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"golang.org/x/term"

	"github.com/prashunchitkr/khelam/internal/agent"
	"github.com/prashunchitkr/khelam/internal/config"
	"github.com/prashunchitkr/khelam/internal/logger"
	"github.com/prashunchitkr/khelam/internal/tui"
	"github.com/prashunchitkr/khelam/internal/version"
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

	if useTUI(agentConfig.TUI) {
		os.Exit(runWithTUI(agentConfig))
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

// useTUI resolves the -tui mode. "auto" enables the TUI only for an
// interactive terminal, so services, containers and pipes get JSON logs.
func useTUI(mode string) bool {
	switch mode {
	case "on":
		return true
	case "off":
		return false
	default:
		return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd()))
	}
}

// runWithTUI runs the agent behind the terminal interface until the user
// quits, and returns the exit status. Logs go to the TUI's log pane.
// When the agent stops on its own, the TUI stays open showing why.
func runWithTUI(cfg config.AgentConfig) int {
	logs := tui.NewLogHandler(slog.LevelDebug)
	logger := slog.New(logs)

	agt := agent.New(agent.Options{
		Logger: logger,
		Cfg:    cfg,
	})

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting khelam agent", "version", version.String(), "agentID", cfg.AgentID)

	result := make(chan error, 1)
	go func() { result <- agt.Run(ctx) }()

	program := tea.NewProgram(tui.New(agt, version.String()), tea.WithContext(ctx))
	logs.Attach(program)

	_, uiErr := program.Run()
	stop()
	runErr := <-result

	if uiErr != nil && !errors.Is(uiErr, tea.ErrProgramKilled) {
		fmt.Fprintln(os.Stderr, "terminal interface failed:", uiErr)
	}

	if runErr != nil {
		fmt.Fprintln(os.Stderr, "agent stopped:", runErr)
		return exitPermanent
	}
	return 0
}
