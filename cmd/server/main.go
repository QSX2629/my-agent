package main

import (
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"my-agent/internal/agent"
	"my-agent/internal/auth"
	"my-agent/internal/config"
	"my-agent/internal/httpapi"
	"my-agent/internal/model"
	"my-agent/internal/store"
)

func main() {
	logger := slog.New(
		slog.NewJSONHandler(os.Stdout, nil),
	)
	slog.SetDefault(logger)

	if err := run(); err != nil {
		slog.Error("app.failed", "error", err)
		os.Exit(1)
	}
}

func run() error {
	root := flag.String("root", ".", "project root directory")
	flag.Parse()

	cfg, err := config.Load(*root)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	messageStore, err := store.Open(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer messageStore.Close()

	if err := messageStore.MigrateSessionOwners(); err != nil {
		return fmt.Errorf("migrate session owners: %w", err)
	}

	modelClient := model.NewClient(
		cfg.APIKey,
		cfg.ModelBaseURL,
		cfg.ModelName,
	)

	modelClient.HTTPClient.Timeout = cfg.ModelTimeout

	agentService := agent.NewService(
		modelClient,
		messageStore,
	)

	authService := auth.NewService(messageStore)

	router := httpapi.NewRouter(
		agentService,
		messageStore,
		authService,
	)

	server := &http.Server{
		Addr:              cfg.Address(),
		Handler:           router,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	slog.Info(
		"server.starting",
		"address", cfg.Address(),
		"model", cfg.ModelName,
		"modelTimeoutSeconds", cfg.ModelTimeout.Seconds(),
		"databasePath", cfg.DatabasePath,
	)

	if err := server.ListenAndServe(); err != nil &&
		!errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("start HTTP server: %w", err)
	}

	return nil
}
