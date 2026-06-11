// Package companies gestiona las empresas de cada organización. Solo
// Administradores las gestionan directamente; las vacantes pueden
// crear una compañía por nombre (find-or-create) al publicarse.
package companies

import (
	"context"
	"errors"
	"net/http"
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

type Company struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	VacancyCount int       `json:"vacancyCount"`
	CreatedAt    time.Time `json:"createdAt"`
}

var ErrNameTaken = errors.New("ya existe una compañía con ese nombre")

type Service struct {
	pool  *pgxpool.Pool
	audit *audit.Logger
}

func NewService(pool *pgxpool.Pool, auditLog *audit.Logger) *Service {
	return &Service{pool: pool, audit: auditLog}
}

func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Company, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.name, count(v.id) AS vacancy_count, c.created_at
		  FROM companies c
		  LEFT JOIN vacancies v ON v.company_id = c.id
		 WHERE c.organization_id = $1
		 GROUP BY c.id
		 ORDER BY c.name`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []Company{}
	for rows.Next() {
		var c Company
		if err := rows.Scan(&c.ID, &c.Name, &c.VacancyCount, &c.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, c)
	}
	return result, rows.Err()
}

func validateName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if len(name) < 2 || len(name) > 120 {
		return "", errors.New("el nombre debe tener entre 2 y 120 caracteres")
	}
	return name, nil
}

func (s *Service) Create(ctx context.Context, actor auth.Identity, name, ip string) (*Company, error) {
	name, err := validateName(name)
	if err != nil {
		return nil, err
	}
	var c Company
	err = s.pool.QueryRow(ctx, `
		INSERT INTO companies (organization_id, name, created_by)
		VALUES ($1, $2, $3)
		RETURNING id, name, 0, created_at`,
		actor.OrganizationID, name, actor.RecruiterID,
	).Scan(&c.ID, &c.Name, &c.VacancyCount, &c.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return nil, ErrNameTaken
		}
		return nil, err
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "companies.create",
		Entity: "company", EntityID: c.ID.String(),
		Detail: map[string]any{"name": c.Name}, IP: ip})
	return &c, nil
}

func (s *Service) Rename(ctx context.Context, actor auth.Identity, id uuid.UUID, name, ip string) error {
	name, err := validateName(name)
	if err != nil {
		return err
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE companies SET name = $1, updated_at = now()
		 WHERE id = $2 AND organization_id = $3`,
		name, id, actor.OrganizationID)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return ErrNameTaken
		}
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "companies.rename",
		Entity: "company", EntityID: id.String(),
		Detail: map[string]any{"name": name}, IP: ip})
	return nil
}

// FindOrCreate localiza una compañía por nombre dentro de la organización o
// la crea. Lo usa el dominio de vacantes al crear/publicar.
func FindOrCreate(ctx context.Context, pool *pgxpool.Pool, orgID uuid.UUID, name string, createdBy uuid.UUID) (uuid.UUID, error) {
	name, err := validateName(name)
	if err != nil {
		return uuid.Nil, err
	}
	var id uuid.UUID
	err = pool.QueryRow(ctx, `
		INSERT INTO companies (organization_id, name, created_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (organization_id, name) DO UPDATE SET updated_at = companies.updated_at
		RETURNING id`,
		orgID, name, createdBy).Scan(&id)
	return id, err
}

// ---- Handlers (RequireAuth + RequireAdmin) ----

type Handlers struct{ service *Service }

func NewHandlers(service *Service) *Handlers { return &Handlers{service: service} }

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	list, err := h.service.List(r.Context(), identity.OrganizationID)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string][]Company{"companies": list})
}

func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	var body struct {
		Name string `json:"name"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	company, err := h.service.Create(r.Context(), identity, body.Name, httpserver.ClientIP(r))
	switch {
	case errors.Is(err, ErrNameTaken):
		web.RespondError(w, http.StatusConflict, "name_taken", err.Error())
	case err != nil:
		web.RespondError(w, http.StatusBadRequest, "validation", err.Error())
	default:
		web.RespondJSON(w, http.StatusCreated, map[string]*Company{"company": company})
	}
}

func (h *Handlers) Rename(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "ID inválido")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	err = h.service.Rename(r.Context(), identity, id, body.Name, httpserver.ClientIP(r))
	switch {
	case errors.Is(err, ErrNameTaken):
		web.RespondError(w, http.StatusConflict, "name_taken", err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		web.RespondError(w, http.StatusNotFound, "not_found", "Compañía no encontrada")
	case err != nil:
		web.RespondError(w, http.StatusBadRequest, "validation", err.Error())
	default:
		web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
