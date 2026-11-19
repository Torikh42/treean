// Package main is the HTTP server entrypoint for the event-ticketing API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/torikh42/treean/internal/config"
	"github.com/torikh42/treean/internal/handler"
	"github.com/torikh42/treean/internal/repository"
	"github.com/torikh42/treean/internal/service"
	"github.com/torikh42/treean/internal/worker"
)

const (
	defaultReadHeaderTimeout    = 5 * time.Second
	defaultIdleTimeout          = 60 * time.Second
	defaultShutdownTimeout      = 10 * time.Second
	defaultExpirySweepBatchSize = 50
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if err := run(logger); err != nil {
		logger.Error("application exited with error", slog.String("error", err.Error()))
		os.Exit(1)
	}
}

// run initializes dependencies, runs migrations, starts background workers, and serves HTTP traffic.
func run(logger *slog.Logger) error {
	logger = safeLogger(logger)

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	pool, err := repository.NewPool(rootCtx, cfg)
	if err != nil {
		return fmt.Errorf("connecting database: %w", err)
	}
	defer pool.Close()

	if err := repository.ApplyMigrations(rootCtx, pool); err != nil {
		return fmt.Errorf("applying migrations: %w", err)
	}
	logger.Info("database migrations and demo seed applied")

	h := buildHandler(logger, cfg, pool)
	srv := buildServer(cfg, h)

	ln, err := (&net.ListenConfig{}).Listen(rootCtx, "tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", srv.Addr, err)
	}

	orderRepo := repository.NewOrderRepo(pool)
	sweeper := worker.NewExpirySweeper(orderRepo, logger, cfg.ExpirySweepInterval, defaultExpirySweepBatchSize)

	workerCtx, cancelWorkers := context.WithCancel(rootCtx)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sweeper.Start(workerCtx)
	}()
	// LIFO defers cancel workers and wait for exit before pool.Close executes.
	defer wg.Wait()
	defer cancelWorkers()

	if err := serveWithGracefulShutdown(rootCtx, logger, srv, ln); err != nil {
		return err
	}

	cancelWorkers()
	wg.Wait()
	logger.Info("graceful shutdown complete")
	return nil
}

// buildHandler wires repositories, services, and the Huma/Chi router without touching a live pool at construction time.
func buildHandler(logger *slog.Logger, cfg config.Config, pool *pgxpool.Pool) http.Handler {
	userRepo := repository.NewUserRepo(pool)
	eventRepo := repository.NewEventRepo(pool)
	orderRepo := repository.NewOrderRepo(pool)
	webhookRepo := repository.NewWebhookRepo(pool)

	authSvc := service.NewAuthService(userRepo, cfg.JWTSecret)
	eventSvc := service.NewEventService(eventRepo, orderRepo)
	orderSvc := service.NewOrderService(orderRepo, eventRepo)
	paymentSvc := service.NewPaymentService(orderRepo, webhookRepo, eventRepo, cfg.WebhookSecret)
	demoSvc := service.NewDemoService(userRepo, eventRepo, orderSvc)

	var db handler.DBPinger
	if pool != nil {
		db = pool
	}

	return handler.NewRouter(handler.RouterDeps{
		Logger:              safeLogger(logger),
		DB:                  db,
		AuthService:         authSvc,
		EventService:        eventSvc,
		OrderService:        orderSvc,
		PaymentService:      paymentSvc,
		DemoService:         demoSvc,
		AllowDemoHeaderAuth: cfg.AllowDemoHeaderAuth,
		CORSAllowedOrigins:  cfg.CORSAllowedOrigins,
	})
}

// buildServer constructs an http.Server with explicit header, read, write, and idle timeouts.
func buildServer(cfg config.Config, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           h,
		ReadHeaderTimeout: defaultReadHeaderTimeout,
		ReadTimeout:       cfg.HTTPReadTimeout,
		WriteTimeout:      cfg.HTTPWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
	}
}

// serveWithGracefulShutdown serves ln until ctx is cancelled or the listener fails, then drains active requests.
func serveWithGracefulShutdown(
	ctx context.Context,
	logger *slog.Logger,
	srv *http.Server,
	ln net.Listener,
) error {
	logger = safeLogger(logger)
	if srv == nil {
		return errors.New("http server is required")
	}
	if ln == nil {
		return errors.New("network listener is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("http server listening", slog.String("addr", ln.Addr().String()))
		serveErr := srv.Serve(ln)
		if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
			errCh <- serveErr
		}
		close(errCh)
	}()

	select {
	case serveErr, ok := <-errCh:
		if ok && serveErr != nil {
			return fmt.Errorf("serving http: %w", serveErr)
		}
		return nil
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), defaultShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		closeErr := srv.Close()
		<-errCh
		return fmt.Errorf("shutting down http server: %w", errors.Join(err, closeErr))
	}

	if serveErr, ok := <-errCh; ok && serveErr != nil {
		return fmt.Errorf("serving http: %w", serveErr)
	}
	return nil
}

// safeLogger guarantees a non-nil slog.Logger by falling back to a discard handler.
func safeLogger(logger *slog.Logger) *slog.Logger {
	if logger == nil {
		return slog.New(slog.DiscardHandler)
	}
	return logger
}
