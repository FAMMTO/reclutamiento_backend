// cmd/worker es el proceso de jobs en segundo plano de Jobbly.
// Procesa la cola River de Postgres: publicación de anuncios en Meta,
// sincronización de estados, etc. Solo ensambla; cero lógica de negocio.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/FAMMTO/reclutamiento_backend/internal/facebookads"
	"github.com/FAMMTO/reclutamiento_backend/internal/jobs"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/config"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/crypto"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/db"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("worker terminó con error", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	// Migraciones de la app
	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}

	// Migraciones de River (schema de la cola)
	migrator, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: log})
	if err != nil {
		return err
	}
	if _, err := migrator.Migrate(ctx, rivermigrate.DirectionUp, nil); err != nil {
		return err
	}

	enc, err := crypto.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	fbSvc := facebookads.NewService(pool, enc, cfg.FacebookRedirectURI, cfg.FacebookFrontendURL)

	workers := river.NewWorkers()
	river.AddWorker(workers, jobs.NewPublishAdWorker(fbSvc))

	riverClient, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Workers: workers,
		Queues: map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: 5},
		},
		Logger: log,
	})
	if err != nil {
		return err
	}

	if err := riverClient.Start(ctx); err != nil {
		return err
	}
	log.Info("worker iniciado", "queues", []string{river.QueueDefault})

	<-ctx.Done()
	log.Info("apagando worker…")

	// Stop con contexto fresco (el original ya está cancelado)
	stopCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return riverClient.StopAndCancel(stopCtx)
}

// Verificar en compile-time que facebookads.Service implementa jobs.AdPublisher.
var _ jobs.AdPublisher = (*facebookads.Service)(nil)

// Verificar que pgx.Tx satisface la constraint del driver (evita sorpresas en runtime).
var _ = (*river.Client[pgx.Tx])(nil)
