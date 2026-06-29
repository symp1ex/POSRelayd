package main

import (
	"context"
	"os"
	"os/signal"
	"rdagent/internal/diag"
	"syscall"

	"rdagent/internal/app"
	"rdagent/internal/config"
	"rdagent/internal/logger"
)

const version = "0.2.4.2"

func main() {
	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		_, _ = os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(2)
	}

	logger.Configure(cfg.LogDir, cfg.LogLevel, cfg.LogRetainDays)

	logger.RDAgent.Infof("rd-agent v%s starting...", version)
	logger.RDAgent.Debug(diag.CurrentTokenReport())
	logger.RDAgent.Infof(
		"Config: ws_url=%s client_id=%s session_id=%s instance_id=%s log_dir=%s log_level=%s log_retain_days=%d",
		cfg.WSURL,
		cfg.ClientID,
		cfg.SessionID,
		cfg.InstanceID,
		cfg.LogDir,
		cfg.LogLevel,
		cfg.LogRetainDays,
	)

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	if err := app.Run(ctx, cfg); err != nil {
		logger.RDAgent.Errorf("rd-agent terminated with error: %v", err)
		os.Exit(1)
	}

	logger.RDAgent.Info("rd-agent stopped")
}
