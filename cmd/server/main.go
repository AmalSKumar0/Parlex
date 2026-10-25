package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/google/uuid"
	"parlex/internal/auth"
	"parlex/internal/catalog"
	"parlex/internal/config"
	"parlex/internal/gateway"
	"parlex/internal/httpapi"
	"parlex/internal/observability"
	"parlex/internal/provider"
	"parlex/internal/provider/mock"
	"parlex/internal/provider/realtime"
	"parlex/internal/store"
)

func main() {
	runMigrations := flag.Bool("migrate", false, "run database migrations and exit")
	flag.Parse()

	cfg, err := config.LoadFromEnv()
	if err != nil {
		slog.Error("configuration error", slog.Any("error", err))
		os.Exit(1)
	}

	logger := observability.NewLogger(cfg.LogLevel)
	slog.SetDefault(logger)

	if *runMigrations {
		logger.Info("running database migrations...")
		if err := store.RunMigrations(cfg.DatabaseURL); err != nil {
			logger.Error("migration failed", slog.Any("error", err))
			os.Exit(1)
		}
		logger.Info("migrations completed successfully")
		return
	}

	// Always ensure database is migrated
	if err := store.RunMigrations(cfg.DatabaseURL); err != nil {
		logger.Warn("initial migration run reported notice", slog.Any("error", err))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db, err := store.NewDB(ctx, cfg.DatabaseURL)
	if err != nil {
		logger.Error("failed connecting to database", slog.Any("error", err))
		os.Exit(1)
	}
	defer db.Close()

	tm, err := auth.NewTokenManager(cfg.JWTSigningKeyFile, cfg.JWTRotationKeyFile)
	if err != nil {
		logger.Error("failed initializing token manager", slog.Any("error", err))
		os.Exit(1)
	}

	rev := auth.NewRevocationRegistry(logger, func(c context.Context) ([]uuid.UUID, error) {
		return db.Queries.ListRevokedDeviceIDs(c)
	})
	rev.StartBackgroundPoller(ctx, 5*time.Second)

	cat, err := catalog.LoadFromFile("config/languages.yaml")
	if err != nil {
		logger.Error("failed loading language catalog", slog.Any("error", err))
		os.Exit(1)
	}

	metrics := observability.NewMetrics()
	internalSrv := observability.StartInternalServer(ctx, cfg.InternalAddr, metrics)
	logger.Info("internal server started", slog.String("addr", cfg.InternalAddr))

	provRouter := provider.NewRouter(cat)
	mockAdapter := mock.New(mock.Config{EchoDelay: 50 * time.Millisecond})
	provRouter.Register(mockAdapter)

	if cfg.ProviderRealtimeAPIKey != "" {
		realtimeAdapter := realtime.New(realtime.Config{
			BaseURL:  cfg.ProviderRealtimeBaseURL,
			APIKey:   cfg.ProviderRealtimeAPIKey,
			AuthMode: cfg.ProviderAuthMode,
		})
		breaker := provider.NewCircuitBreaker(5, 30*time.Second, 20*time.Second)
		provRouter.Register(provider.WrapWithBreaker(realtimeAdapter, breaker))
		logger.Info("realtime cloud translation provider registered")
	}

	gw := gateway.NewGateway(cfg, db, tm, rev, provRouter, metrics, logger)
	handlers := httpapi.NewHandlers(cfg, db, tm, rev, cat, gw, logger)
	router := httpapi.NewRouter(cfg, handlers, tm, rev, gw.ServeHTTP, logger)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	serverErr := make(chan error, 1)
	go func() {
		logger.Info("public http server listening", slog.String("addr", cfg.HTTPAddr))
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErr <- err
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-serverErr:
		logger.Error("http server failed", slog.Any("error", err))
		os.Exit(1)
	case sig := <-sigCh:
		logger.Info("shutdown signal received", slog.String("signal", sig.String()))
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful server shutdown failed", slog.Any("error", err))
	}
	if err := gw.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful gateway shutdown failed", slog.Any("error", err))
	}
	if err := internalSrv.Shutdown(shutdownCtx); err != nil {
		logger.Error("graceful internal server shutdown failed", slog.Any("error", err))
	}

	logger.Info("server shutdown complete")
}
