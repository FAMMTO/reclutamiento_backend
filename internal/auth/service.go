// Package auth implementa autenticación de reclutadores: login con argon2id,
// access tokens JWT de corta vida, refresh tokens opacos rotativos con
// detección de reuso (robo), logout y cambio de contraseña obligatorio.
package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/platform/audit"
)

var (
	ErrInvalidCredentials = errors.New("credenciales inválidas")
	ErrInvalidRefresh     = errors.New("sesión inválida o expirada")
	ErrWeakPassword       = errors.New("contraseña débil")
	ErrSamePassword       = errors.New("la nueva contraseña debe ser distinta a la actual")
)

type Recruiter struct {
	ID              uuid.UUID `json:"id"`
	OrganizationID  uuid.UUID `json:"organizationId"`
	Name            string    `json:"name"`
	Email           string    `json:"email"`
	Phone           string    `json:"phone"`
	Permission      string    `json:"permission"`
	IsActive        bool      `json:"isActive"`
	PasswordChanged bool      `json:"passwordChanged"`
	CreatedAt       time.Time `json:"createdAt"`
}

type Service struct {
	pool            *pgxpool.Pool
	audit           *audit.Logger
	jwtSecret       []byte
	accessTokenTTL  time.Duration
	refreshTokenTTL time.Duration
}

func NewService(pool *pgxpool.Pool, auditLog *audit.Logger, jwtSecret []byte, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{
		pool:            pool,
		audit:           auditLog,
		jwtSecret:       jwtSecret,
		accessTokenTTL:  accessTTL,
		refreshTokenTTL: refreshTTL,
	}
}

type Session struct {
	AccessToken  string
	RefreshToken string
	Recruiter    Recruiter
}

const recruiterColumns = `id, organization_id, name, email, phone, permission,
	is_active, password_changed, created_at`

func scanRecruiter(row pgx.Row) (Recruiter, string, error) {
	var rec Recruiter
	var hash string
	err := row.Scan(&rec.ID, &rec.OrganizationID, &rec.Name, &rec.Email, &rec.Phone,
		&rec.Permission, &rec.IsActive, &rec.PasswordChanged, &rec.CreatedAt, &hash)
	return rec, hash, err
}

func (s *Service) Login(ctx context.Context, email, password, ip string) (*Session, error) {
	email = strings.TrimSpace(strings.ToLower(email))
	row := s.pool.QueryRow(ctx,
		`SELECT `+recruiterColumns+`, password_hash
		   FROM recruiters WHERE email = $1 AND is_active = true`, email)
	rec, hash, err := scanRecruiter(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// igualar el costo de la respuesta para no revelar si el email existe
			_, _ = VerifyPassword(password, dummyHash)
			s.audit.Record(ctx, audit.Entry{Action: "auth.login_failed", Entity: "recruiter",
				Detail: map[string]any{"email": email, "reason": "not_found"}, IP: ip})
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}

	ok, err := VerifyPassword(password, hash)
	if err != nil || !ok {
		s.audit.Record(ctx, audit.Entry{ActorID: &rec.ID, Action: "auth.login_failed",
			Entity: "recruiter", EntityID: rec.ID.String(),
			Detail: map[string]any{"reason": "bad_password"}, IP: ip})
		return nil, ErrInvalidCredentials
	}

	session, err := s.issueSession(ctx, rec)
	if err != nil {
		return nil, err
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &rec.ID, Action: "auth.login",
		Entity: "recruiter", EntityID: rec.ID.String(), IP: ip})
	return session, nil
}

// dummyHash se usa para que el login con email inexistente tarde lo mismo
// que uno con contraseña incorrecta (evita enumeración por timing).
var dummyHash = func() string {
	h, _ := HashPassword("dummy-timing-equalizer")
	return h
}()

func (s *Service) issueSession(ctx context.Context, rec Recruiter) (*Session, error) {
	accessToken, err := NewAccessToken(s.jwtSecret, s.accessTokenTTL,
		rec.ID, rec.OrganizationID, rec.Permission, rec.PasswordChanged)
	if err != nil {
		return nil, err
	}
	refreshToken, refreshHash, err := NewRefreshToken()
	if err != nil {
		return nil, err
	}
	_, err = s.pool.Exec(ctx,
		`INSERT INTO refresh_tokens (recruiter_id, token_hash, expires_at)
		 VALUES ($1, $2, $3)`,
		rec.ID, refreshHash, time.Now().Add(s.refreshTokenTTL))
	if err != nil {
		return nil, err
	}
	return &Session{AccessToken: accessToken, RefreshToken: refreshToken, Recruiter: rec}, nil
}

// Refresh rota el token: revoca el presentado y emite uno nuevo. Si se
// presenta un token ya revocado (reuso ⇒ posible robo), revoca todas las
// sesiones del usuario.
func (s *Service) Refresh(ctx context.Context, refreshToken, ip string) (*Session, error) {
	tokenHash := HashRefreshToken(refreshToken)

	var recruiterID uuid.UUID
	var expiresAt time.Time
	var revokedAt *time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT recruiter_id, expires_at, revoked_at
		   FROM refresh_tokens WHERE token_hash = $1`, tokenHash,
	).Scan(&recruiterID, &expiresAt, &revokedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrInvalidRefresh
		}
		return nil, err
	}

	if revokedAt != nil {
		_, _ = s.pool.Exec(ctx,
			`UPDATE refresh_tokens SET revoked_at = now()
			  WHERE recruiter_id = $1 AND revoked_at IS NULL`, recruiterID)
		s.audit.Record(ctx, audit.Entry{ActorID: &recruiterID, Action: "auth.refresh_reuse_detected",
			Entity: "recruiter", EntityID: recruiterID.String(), IP: ip})
		return nil, ErrInvalidRefresh
	}
	if time.Now().After(expiresAt) {
		return nil, ErrInvalidRefresh
	}

	row := s.pool.QueryRow(ctx,
		`SELECT `+recruiterColumns+`, password_hash
		   FROM recruiters WHERE id = $1 AND is_active = true`, recruiterID)
	rec, _, err := scanRecruiter(row)
	if err != nil {
		return nil, ErrInvalidRefresh
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now() WHERE token_hash = $1`, tokenHash); err != nil {
		return nil, err
	}
	return s.issueSession(ctx, rec)
}

func (s *Service) Logout(ctx context.Context, refreshToken string) {
	if refreshToken == "" {
		return
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		  WHERE token_hash = $1 AND revoked_at IS NULL`,
		HashRefreshToken(refreshToken))
}

func (s *Service) GetRecruiter(ctx context.Context, id uuid.UUID) (Recruiter, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+recruiterColumns+`, password_hash
		   FROM recruiters WHERE id = $1 AND is_active = true`, id)
	rec, _, err := scanRecruiter(row)
	return rec, err
}

// ChangePassword valida la actual, aplica la política, marca password_changed
// y revoca todas las sesiones previas, emitiendo una nueva.
func (s *Service) ChangePassword(ctx context.Context, recruiterID uuid.UUID, current, next, ip string) (*Session, error) {
	row := s.pool.QueryRow(ctx,
		`SELECT `+recruiterColumns+`, password_hash
		   FROM recruiters WHERE id = $1 AND is_active = true`, recruiterID)
	rec, hash, err := scanRecruiter(row)
	if err != nil {
		return nil, ErrInvalidCredentials
	}

	ok, err := VerifyPassword(current, hash)
	if err != nil || !ok {
		s.audit.Record(ctx, audit.Entry{ActorID: &rec.ID, Action: "auth.change_password_failed",
			Entity: "recruiter", EntityID: rec.ID.String(), IP: ip})
		return nil, ErrInvalidCredentials
	}
	if current == next {
		return nil, ErrSamePassword
	}
	if err := ValidateNewPassword(next); err != nil {
		return nil, errors.Join(ErrWeakPassword, err)
	}

	newHash, err := HashPassword(next)
	if err != nil {
		return nil, err
	}
	if _, err := s.pool.Exec(ctx,
		`UPDATE recruiters
		    SET password_hash = $1, password_changed = true, updated_at = now()
		  WHERE id = $2`, newHash, rec.ID); err != nil {
		return nil, err
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE refresh_tokens SET revoked_at = now()
		  WHERE recruiter_id = $1 AND revoked_at IS NULL`, rec.ID)

	rec.PasswordChanged = true
	s.audit.Record(ctx, audit.Entry{ActorID: &rec.ID, Action: "auth.change_password",
		Entity: "recruiter", EntityID: rec.ID.String(), IP: ip})
	return s.issueSession(ctx, rec)
}
