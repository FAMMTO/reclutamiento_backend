// Package rutas gestiona las rutas de transporte y su vínculo N:M con
// vacantes (reemplaza el localStorage "Jobbly-rutas" del frontend).
package rutas

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/auth"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/audit"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/httpserver"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/web"
)

type Ruta struct {
	ID         uuid.UUID   `json:"id"`
	Ubicacion  string      `json:"ubicacion"`
	Horario    string      `json:"horario"`
	Lat        *float64    `json:"lat"`
	Lng        *float64    `json:"lng"`
	VacancyIDs []uuid.UUID `json:"vacancyIds"`
	CreatedAt  time.Time   `json:"createdAt"`
}

type Input struct {
	Ubicacion  string      `json:"ubicacion"`
	Horario    string      `json:"horario"`
	Lat        *float64    `json:"lat"`
	Lng        *float64    `json:"lng"`
	VacancyIDs []uuid.UUID `json:"vacancyIds"`
}

func (input *Input) validate() error {
	input.Ubicacion = strings.TrimSpace(input.Ubicacion)
	input.Horario = strings.TrimSpace(input.Horario)
	if input.Ubicacion == "" || len(input.Ubicacion) > 200 {
		return errors.New("ubicacion es obligatoria (máx. 200 caracteres)")
	}
	if input.VacancyIDs == nil {
		input.VacancyIDs = []uuid.UUID{}
	}
	return nil
}

type Service struct {
	pool  *pgxpool.Pool
	audit *audit.Logger
}

func NewService(pool *pgxpool.Pool, auditLog *audit.Logger) *Service {
	return &Service{pool: pool, audit: auditLog}
}

func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Ruta, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT r.id, r.ubicacion, r.horario, r.lat, r.lng, r.created_at,
		       coalesce(array_agg(rv.vacancy_id) FILTER (WHERE rv.vacancy_id IS NOT NULL), '{}')
		  FROM rutas r
		  LEFT JOIN ruta_vacancies rv ON rv.ruta_id = r.id
		 WHERE r.organization_id = $1
		 GROUP BY r.id
		 ORDER BY r.created_at DESC`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []Ruta{}
	for rows.Next() {
		var ruta Ruta
		if err := rows.Scan(&ruta.ID, &ruta.Ubicacion, &ruta.Horario, &ruta.Lat,
			&ruta.Lng, &ruta.CreatedAt, &ruta.VacancyIDs); err != nil {
			return nil, err
		}
		result = append(result, ruta)
	}
	return result, rows.Err()
}

// syncVacancies reemplaza los vínculos de la ruta validando que las vacantes
// pertenezcan a la misma organización.
func (s *Service) syncVacancies(ctx context.Context, tx pgx.Tx, orgID, rutaID uuid.UUID, vacancyIDs []uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		`DELETE FROM ruta_vacancies WHERE ruta_id = $1`, rutaID); err != nil {
		return err
	}
	if len(vacancyIDs) == 0 {
		return nil
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO ruta_vacancies (ruta_id, vacancy_id)
		SELECT $1, v.id FROM vacancies v
		 WHERE v.id = ANY($2) AND v.organization_id = $3`,
		rutaID, vacancyIDs, orgID)
	if err != nil {
		return err
	}
	if int(tag.RowsAffected()) != len(vacancyIDs) {
		return errors.New("una o más vacantes no existen en esta organización")
	}
	return nil
}

func (s *Service) Create(ctx context.Context, actor auth.Identity, input Input, ip string) (*Ruta, error) {
	if err := input.validate(); err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ruta Ruta
	err = tx.QueryRow(ctx, `
		INSERT INTO rutas (organization_id, ubicacion, horario, lat, lng, created_by)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING id, ubicacion, horario, lat, lng, created_at`,
		actor.OrganizationID, input.Ubicacion, input.Horario, input.Lat, input.Lng,
		actor.RecruiterID,
	).Scan(&ruta.ID, &ruta.Ubicacion, &ruta.Horario, &ruta.Lat, &ruta.Lng, &ruta.CreatedAt)
	if err != nil {
		return nil, err
	}
	if err := s.syncVacancies(ctx, tx, actor.OrganizationID, ruta.ID, input.VacancyIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	ruta.VacancyIDs = input.VacancyIDs

	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "rutas.create",
		Entity: "ruta", EntityID: ruta.ID.String(),
		Detail: map[string]any{"ubicacion": ruta.Ubicacion}, IP: ip})
	return &ruta, nil
}

func (s *Service) Update(ctx context.Context, actor auth.Identity, id uuid.UUID, input Input, ip string) error {
	if err := input.validate(); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		UPDATE rutas SET ubicacion = $1, horario = $2, lat = $3, lng = $4, updated_at = now()
		 WHERE id = $5 AND organization_id = $6`,
		input.Ubicacion, input.Horario, input.Lat, input.Lng, id, actor.OrganizationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if err := s.syncVacancies(ctx, tx, actor.OrganizationID, id, input.VacancyIDs); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "rutas.update",
		Entity: "ruta", EntityID: id.String(), IP: ip})
	return nil
}

func (s *Service) Delete(ctx context.Context, actor auth.Identity, id uuid.UUID, ip string) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM rutas WHERE id = $1 AND organization_id = $2`,
		id, actor.OrganizationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "rutas.delete",
		Entity: "ruta", EntityID: id.String(), IP: ip})
	return nil
}

// ---- Handlers (RequireAuth) ----

type Handlers struct{ service *Service }

func NewHandlers(service *Service) *Handlers { return &Handlers{service: service} }

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	list, err := h.service.List(r.Context(), identity.OrganizationID)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string][]Ruta{"rutas": list})
}

func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	var input Input
	if err := web.DecodeJSON(w, r, &input); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	ruta, err := h.service.Create(r.Context(), identity, input, httpserver.ClientIP(r))
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	web.RespondJSON(w, http.StatusCreated, map[string]*Ruta{"ruta": ruta})
}

func parseID(r *http.Request) (uuid.UUID, error) {
	return uuid.Parse(chi.URLParam(r, "id"))
}

func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	id, err := parseID(r)
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "ID inválido")
		return
	}
	var input Input
	if err := web.DecodeJSON(w, r, &input); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	err = h.service.Update(r.Context(), identity, id, input, httpserver.ClientIP(r))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		web.RespondError(w, http.StatusNotFound, "not_found", "Ruta no encontrada")
	case err != nil:
		web.RespondError(w, http.StatusBadRequest, "validation", err.Error())
	default:
		web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	id, err := parseID(r)
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "ID inválido")
		return
	}
	err = h.service.Delete(r.Context(), identity, id, httpserver.ClientIP(r))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		web.RespondError(w, http.StatusNotFound, "not_found", "Ruta no encontrada")
	case err != nil:
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
	default:
		web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
