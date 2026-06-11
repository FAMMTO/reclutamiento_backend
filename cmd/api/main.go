// cmd/api es el entrypoint del servidor HTTP de Jobbly: solo ensambla
// configuración, base de datos, dominios y rutas. Cero lógica de negocio.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/auth"
	"github.com/FAMMTO/reclutamiento_backend/internal/candidates"
	"github.com/FAMMTO/reclutamiento_backend/internal/companies"
	"github.com/FAMMTO/reclutamiento_backend/internal/facebookads"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/audit"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/config"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/crypto"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/db"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/httpserver"
	"github.com/FAMMTO/reclutamiento_backend/internal/recruiters"
	"github.com/FAMMTO/reclutamiento_backend/internal/rutas"
	"github.com/FAMMTO/reclutamiento_backend/internal/vacancies"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	if err := run(log); err != nil {
		log.Error("el servidor terminó con error", "err", err)
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

	if err := db.Migrate(ctx, pool, log); err != nil {
		return err
	}
	if err := seedInitialAdmin(ctx, pool, cfg, log); err != nil {
		return err
	}

	enc, err := crypto.New(cfg.EncryptionKey)
	if err != nil {
		return err
	}

	auditLog := audit.New(pool, log)
	authService := auth.NewService(pool, auditLog, cfg.JWTSecret, cfg.AccessTokenTTL, cfg.RefreshTokenTTL)
	authHandlers := auth.NewHandlers(authService, cfg.CookieSecure, cfg.CookieDomain, cfg.RefreshTokenTTL)
	recruiterHandlers := recruiters.NewHandlers(recruiters.NewService(pool, auditLog))
	companyHandlers := companies.NewHandlers(companies.NewService(pool, auditLog))
	vacancyHandlers := vacancies.NewHandlers(vacancies.NewService(pool, auditLog))
	rutaHandlers := rutas.NewHandlers(rutas.NewService(pool, auditLog))
	candidateHandlers := candidates.NewHandlers(candidates.NewService(pool, auditLog))
	fbHandlers := facebookads.NewHandlers(facebookads.NewService(pool, enc))

	router := chi.NewRouter()
	router.Use(httpserver.Recover(log))
	router.Use(httpserver.RequestLogger(log))
	router.Use(httpserver.SecurityHeaders)
	router.Use(httpserver.CORS(cfg.CORSOrigins))
	router.Use(httpserver.RateLimit(300, 60)) // límite global por IP

	router.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})
	router.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "db no disponible", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	router.Route("/api/v1", func(api chi.Router) {
		api.Route("/auth", func(authRouter chi.Router) {
			// límite estricto solo para login (fuerza bruta sobre credenciales);
			// refresh ocurre en cada carga de página y necesita margen normal
			authRouter.Group(func(strict chi.Router) {
				strict.Use(httpserver.RateLimit(10, 5))
				strict.Post("/login", authHandlers.Login)
			})
			authRouter.Group(func(normal chi.Router) {
				normal.Use(httpserver.RateLimit(60, 20))
				normal.Post("/refresh", authHandlers.Refresh)
			})
			authRouter.Post("/logout", authHandlers.Logout)

			authRouter.Group(func(protected chi.Router) {
				protected.Use(auth.RequireAuth(cfg.JWTSecret))
				protected.Get("/me", authHandlers.Me)
				protected.Post("/change-password", authHandlers.ChangePassword)
			})
		})

		// endpoints públicos del flujo de candidatos (sin auth, rate limit propio)
		api.Route("/public", func(public chi.Router) {
			public.Group(func(reads chi.Router) {
				reads.Use(httpserver.RateLimit(120, 40))
				reads.Get("/vacancies", vacancyHandlers.PublicList)
				reads.Get("/vacancies/{id}", vacancyHandlers.PublicGet)
			})
			public.Group(func(writes chi.Router) {
				writes.Use(httpserver.RateLimit(10, 5)) // anti-spam de postulaciones
				writes.Post("/applications", candidateHandlers.Apply)
			})
		})

		api.Group(func(protected chi.Router) {
			protected.Use(auth.RequireAuth(cfg.JWTSecret))
			protected.Use(auth.RequirePasswordChanged)

			// todos los roles
			protected.Post("/vacancies", vacancyHandlers.Create)
			protected.Get("/vacancies", vacancyHandlers.List)
			protected.Get("/vacancies/{id}", vacancyHandlers.Get)
			protected.Patch("/vacancies/{id}", vacancyHandlers.SetStatus)

			protected.Get("/candidates", candidateHandlers.ListCandidates)
			protected.Get("/applications", candidateHandlers.ListApplications)
			protected.Patch("/applications/{id}", candidateHandlers.SetApplicationStatus)

			protected.Get("/rutas", rutaHandlers.List)
			protected.Post("/rutas", rutaHandlers.Create)
			protected.Patch("/rutas/{id}", rutaHandlers.Update)
			protected.Delete("/rutas/{id}", rutaHandlers.Delete)

			// Facebook Ads (todos los roles autenticados)
			protected.Get("/facebookads/config", fbHandlers.GetConfig)
			protected.Put("/facebookads/config", fbHandlers.SaveConfig)
			protected.Post("/facebookads/test", fbHandlers.TestConnection)
			protected.Get("/facebookads/ads", fbHandlers.ListAds)
			protected.Post("/facebookads/ads", fbHandlers.CreateAd)
			protected.Patch("/facebookads/ads/{id}", fbHandlers.SetAdStatus)
			protected.Delete("/facebookads/ads/{id}", fbHandlers.DeleteAd)

			// solo Administrador (matriz de permisos)
			protected.Group(func(admin chi.Router) {
				admin.Use(auth.RequireAdmin)
				admin.Get("/recruiters", recruiterHandlers.List)
				admin.Post("/recruiters", recruiterHandlers.Create)
				admin.Patch("/recruiters/{id}", recruiterHandlers.SetActive)

				admin.Get("/companies", companyHandlers.List)
				admin.Post("/companies", companyHandlers.Create)
				admin.Patch("/companies/{id}", companyHandlers.Rename)
			})
		})
	})

	server := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("servidor escuchando", "port", cfg.Port, "env", cfg.Env)
		errCh <- server.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("apagando servidor…")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}

// seedInitialAdmin crea el primer administrador solo si no existe ningún
// reclutador (idempotente). Las credenciales vienen de SEED_ADMIN_EMAIL y
// SEED_ADMIN_PASSWORD; el admin nace con password_changed=false para forzar
// el cambio en su primer login.
func seedInitialAdmin(ctx context.Context, pool *pgxpool.Pool, cfg *config.Config, log *slog.Logger) error {
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM recruiters`).Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
	}
	if cfg.SeedAdminEmail == "" || cfg.SeedAdminPassword == "" {
		log.Warn("no hay reclutadores y no se configuró SEED_ADMIN_EMAIL/SEED_ADMIN_PASSWORD; nadie podrá iniciar sesión")
		return nil
	}
	if err := auth.ValidateTemporaryPassword(cfg.SeedAdminPassword); err != nil {
		return errors.New("SEED_ADMIN_PASSWORD: " + err.Error())
	}
	hash, err := auth.HashPassword(cfg.SeedAdminPassword)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		WITH org AS (
			INSERT INTO organizations (name) VALUES ('Jobbly') RETURNING id
		)
		INSERT INTO recruiters (organization_id, name, email, phone, password_hash, permission)
		SELECT id, $1, $2, '', $3, 'Administrador' FROM org`,
		cfg.SeedAdminName, cfg.SeedAdminEmail, hash)
	if err != nil {
		return err
	}
	log.Info("administrador inicial creado", "email", cfg.SeedAdminEmail)
	return nil
}
