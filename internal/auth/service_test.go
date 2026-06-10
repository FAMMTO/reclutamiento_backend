package auth

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/platform/audit"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/db"
)

// Tests de integración: requieren TEST_DATABASE_URL apuntando a una DB de
// pruebas (su contenido se borra). Si no está definida, se omiten.
func setupTestService(t *testing.T) (*Service, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL no definida; omitiendo tests de integración")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("conectando a la DB de pruebas: %v", err)
	}
	t.Cleanup(pool.Close)

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	if err := db.Migrate(ctx, pool, log); err != nil {
		t.Fatalf("migrando DB de pruebas: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`TRUNCATE audit_log, refresh_tokens, recruiters, organizations CASCADE`); err != nil {
		t.Fatalf("limpiando DB de pruebas: %v", err)
	}

	service := NewService(pool, audit.New(pool, log), testSecret, 15*time.Minute, 24*time.Hour)
	return service, pool
}

func createTestRecruiter(t *testing.T, pool *pgxpool.Pool, email, password string, passwordChanged bool) uuid.UUID {
	t.Helper()
	hash, err := HashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	var id uuid.UUID
	err = pool.QueryRow(context.Background(), `
		WITH org AS (
			INSERT INTO organizations (name) VALUES ('Test Org') RETURNING id
		)
		INSERT INTO recruiters (organization_id, name, email, password_hash, permission, password_changed)
		SELECT id, 'Reclutador Prueba', $1, $2, 'Administrador', $3 FROM org
		RETURNING id`, email, hash, passwordChanged).Scan(&id)
	if err != nil {
		t.Fatalf("creando reclutador de prueba: %v", err)
	}
	return id
}

func TestLoginSuccessAndFailure(t *testing.T) {
	service, pool := setupTestService(t)
	ctx := context.Background()
	createTestRecruiter(t, pool, "admin@test.mx", "Temporal123", true)

	session, err := service.Login(ctx, "Admin@Test.MX", "Temporal123", "127.0.0.1")
	if err != nil {
		t.Fatalf("login válido falló: %v", err)
	}
	if session.AccessToken == "" || session.RefreshToken == "" {
		t.Fatal("sesión sin tokens")
	}
	if session.Recruiter.Email != "admin@test.mx" {
		t.Errorf("email = %s", session.Recruiter.Email)
	}

	if _, err := service.Login(ctx, "admin@test.mx", "incorrecta", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("contraseña incorrecta: esperaba ErrInvalidCredentials, fue %v", err)
	}
	if _, err := service.Login(ctx, "noexiste@test.mx", "loquesea", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("email inexistente: esperaba ErrInvalidCredentials, fue %v", err)
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	service, pool := setupTestService(t)
	ctx := context.Background()
	recruiterID := createTestRecruiter(t, pool, "rotate@test.mx", "Temporal123", true)

	session, err := service.Login(ctx, "rotate@test.mx", "Temporal123", "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	firstRefresh := session.RefreshToken

	// rotación: el refresh emite tokens nuevos y revoca el usado
	rotated, err := service.Refresh(ctx, firstRefresh, "127.0.0.1")
	if err != nil {
		t.Fatalf("refresh válido falló: %v", err)
	}
	if rotated.RefreshToken == firstRefresh {
		t.Fatal("el refresh token no rotó")
	}

	// reuso del token ya rotado ⇒ se revocan TODAS las sesiones del usuario
	if _, err := service.Refresh(ctx, firstRefresh, "127.0.0.1"); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("reuso de token revocado: esperaba ErrInvalidRefresh, fue %v", err)
	}
	if _, err := service.Refresh(ctx, rotated.RefreshToken, "127.0.0.1"); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatal("tras detectar reuso, el token vigente también debe quedar revocado")
	}

	var active int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM refresh_tokens WHERE recruiter_id = $1 AND revoked_at IS NULL`,
		recruiterID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if active != 0 {
		t.Fatalf("quedaron %d refresh tokens activos tras la detección de reuso", active)
	}
}

func TestChangePasswordFlow(t *testing.T) {
	service, pool := setupTestService(t)
	ctx := context.Background()
	recruiterID := createTestRecruiter(t, pool, "nuevo@test.mx", "Temporal123", false)

	// contraseña actual incorrecta
	if _, err := service.ChangePassword(ctx, recruiterID, "incorrecta", "NuevaClave2026", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatalf("esperaba ErrInvalidCredentials, fue %v", err)
	}
	// nueva contraseña débil
	if _, err := service.ChangePassword(ctx, recruiterID, "Temporal123", "corta1", "127.0.0.1"); !errors.Is(err, ErrWeakPassword) {
		t.Fatalf("esperaba ErrWeakPassword, fue %v", err)
	}
	// cambio válido
	session, err := service.ChangePassword(ctx, recruiterID, "Temporal123", "NuevaClave2026", "127.0.0.1")
	if err != nil {
		t.Fatalf("cambio válido falló: %v", err)
	}
	if !session.Recruiter.PasswordChanged {
		t.Fatal("passwordChanged debió quedar en true")
	}

	// la contraseña anterior ya no sirve; la nueva sí
	if _, err := service.Login(ctx, "nuevo@test.mx", "Temporal123", "127.0.0.1"); !errors.Is(err, ErrInvalidCredentials) {
		t.Fatal("la contraseña temporal siguió funcionando tras el cambio")
	}
	if _, err := service.Login(ctx, "nuevo@test.mx", "NuevaClave2026", "127.0.0.1"); err != nil {
		t.Fatalf("login con la nueva contraseña falló: %v", err)
	}
}
