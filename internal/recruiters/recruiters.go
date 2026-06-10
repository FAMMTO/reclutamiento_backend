// Package recruiters implementa el alta y gestión de reclutadores según el
// flujo documentado en DATABASE_VARIABLES.md: solo Administradores crean
// reclutadores, con contraseña temporal que obliga cambio en el primer login.
package recruiters

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/auth"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/audit"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/httpserver"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/web"
)

var (
	nameRe  = regexp.MustCompile(`^[\p{L}][\p{L} ]{1,119}$`)
	emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)
	phoneRe = regexp.MustCompile(`^[0-9+ ]{7,20}$`)
)

type Recruiter struct {
	ID              uuid.UUID  `json:"id"`
	Name            string     `json:"name"`
	Email           string     `json:"email"`
	Phone           string     `json:"phone"`
	Permission      string     `json:"permission"`
	IsActive        bool       `json:"isActive"`
	PasswordChanged bool       `json:"passwordChanged"`
	CreatedBy       *uuid.UUID `json:"createdBy"`
	CreatedAt       time.Time  `json:"createdAt"`
}

type Service struct {
	pool  *pgxpool.Pool
	audit *audit.Logger
}

func NewService(pool *pgxpool.Pool, auditLog *audit.Logger) *Service {
	return &Service{pool: pool, audit: auditLog}
}

const columns = `id, name, email, phone, permission, is_active, password_changed,
	created_by, created_at`

func scan(rows pgx.Rows) (Recruiter, error) {
	var rec Recruiter
	err := rows.Scan(&rec.ID, &rec.Name, &rec.Email, &rec.Phone, &rec.Permission,
		&rec.IsActive, &rec.PasswordChanged, &rec.CreatedBy, &rec.CreatedAt)
	return rec, err
}

func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Recruiter, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+columns+` FROM recruiters
		  WHERE organization_id = $1 ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []Recruiter{}
	for rows.Next() {
		rec, err := scan(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, rec)
	}
	return result, rows.Err()
}

type CreateInput struct {
	Name              string `json:"name"`
	Email             string `json:"email"`
	Phone             string `json:"phone"`
	TemporaryPassword string `json:"temporaryPassword"`
	Permission        string `json:"permission"`
}

var ErrEmailTaken = errors.New("ya existe un reclutador con ese email")

func (input *CreateInput) validate() error {
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	input.Phone = strings.TrimSpace(input.Phone)

	switch {
	case !nameRe.MatchString(input.Name):
		return errors.New("nombre inválido: solo letras y espacios (2-120 caracteres)")
	case !emailRe.MatchString(input.Email):
		return errors.New("email inválido")
	case input.Phone != "" && !phoneRe.MatchString(input.Phone):
		return errors.New("teléfono inválido: solo dígitos, + y espacios")
	case input.Permission != "Administrador" && input.Permission != "Ejecutivo":
		return errors.New(`permission debe ser "Administrador" o "Ejecutivo"`)
	}
	return auth.ValidateTemporaryPassword(input.TemporaryPassword)
}

func (s *Service) Create(ctx context.Context, actor auth.Identity, input CreateInput, ip string) (*Recruiter, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	hash, err := auth.HashPassword(input.TemporaryPassword)
	if err != nil {
		return nil, err
	}

	var rec Recruiter
	err = s.pool.QueryRow(ctx,
		`INSERT INTO recruiters
		    (organization_id, name, email, phone, password_hash, permission, created_by)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+columns,
		actor.OrganizationID, input.Name, input.Email, input.Phone, hash,
		input.Permission, actor.RecruiterID,
	).Scan(&rec.ID, &rec.Name, &rec.Email, &rec.Phone, &rec.Permission,
		&rec.IsActive, &rec.PasswordChanged, &rec.CreatedBy, &rec.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrEmailTaken
		}
		return nil, err
	}

	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "recruiters.create",
		Entity: "recruiter", EntityID: rec.ID.String(),
		Detail: map[string]any{"email": rec.Email, "permission": rec.Permission}, IP: ip})
	return &rec, nil
}

var ErrSelfDeactivation = errors.New("no puedes desactivar tu propia cuenta")

func (s *Service) SetActive(ctx context.Context, actor auth.Identity, id uuid.UUID, active bool, ip string) error {
	if id == actor.RecruiterID && !active {
		return ErrSelfDeactivation
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE recruiters SET is_active = $1, updated_at = now()
		  WHERE id = $2 AND organization_id = $3`,
		active, id, actor.OrganizationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if !active {
		// cerrar sesiones abiertas del usuario desactivado
		_, _ = s.pool.Exec(ctx,
			`UPDATE refresh_tokens SET revoked_at = now()
			  WHERE recruiter_id = $1 AND revoked_at IS NULL`, id)
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "recruiters.set_active",
		Entity: "recruiter", EntityID: id.String(),
		Detail: map[string]any{"isActive": active}, IP: ip})
	return nil
}

// ---- Handlers HTTP (todos detrás de RequireAuth + RequireAdmin) ----

type Handlers struct{ service *Service }

func NewHandlers(service *Service) *Handlers { return &Handlers{service: service} }

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	list, err := h.service.List(r.Context(), identity.OrganizationID)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string][]Recruiter{"recruiters": list})
}

func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	var input CreateInput
	if err := web.DecodeJSON(w, r, &input); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	rec, err := h.service.Create(r.Context(), identity, input, httpserver.ClientIP(r))
	if err != nil {
		if errors.Is(err, ErrEmailTaken) {
			web.RespondError(w, http.StatusConflict, "email_taken", err.Error())
			return
		}
		web.RespondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	web.RespondJSON(w, http.StatusCreated, map[string]*Recruiter{"recruiter": rec})
}

func (h *Handlers) SetActive(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "ID inválido")
		return
	}
	var body struct {
		IsActive *bool `json:"isActive"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil || body.IsActive == nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "isActive (boolean) es obligatorio")
		return
	}
	err = h.service.SetActive(r.Context(), identity, id, *body.IsActive, httpserver.ClientIP(r))
	switch {
	case errors.Is(err, ErrSelfDeactivation):
		web.RespondError(w, http.StatusBadRequest, "self_deactivation", err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		web.RespondError(w, http.StatusNotFound, "not_found", "Reclutador no encontrado")
	case err != nil:
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
	default:
		web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
