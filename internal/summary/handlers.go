// Package summary expone métricas de negocio de la organización autenticada.
// Solo lectura; no toca audit log.
package summary

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/auth"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/web"
)

type Counts struct {
	Candidates int `json:"candidates"`
	Vacancies  struct {
		Draft     int `json:"draft"`
		Published int `json:"published"`
		Closed    int `json:"closed"`
	} `json:"vacancies"`
	Companies  int `json:"companies"`
	Recruiters int `json:"recruiters"`
	Pipeline   struct {
		Nueva      int `json:"nueva"`
		EnRevision int `json:"en_revision"`
		Entrevista int `json:"entrevista"`
		Rechazada  int `json:"rechazada"`
		Contratada int `json:"contratada"`
	} `json:"pipeline"`
}

type Handlers struct {
	pool *pgxpool.Pool
}

func NewHandlers(pool *pgxpool.Pool) *Handlers {
	return &Handlers{pool: pool}
}

func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	id, ok := auth.IdentityFrom(r.Context())
	if !ok {
		web.RespondError(w, http.StatusUnauthorized, "unauthorized", "no autenticado")
		return
	}

	counts, err := h.query(r.Context(), id.OrganizationID)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	web.RespondJSON(w, http.StatusOK, map[string]any{"summary": counts})
}

func (h *Handlers) query(ctx context.Context, orgID uuid.UUID) (*Counts, error) {
	var c Counts

	// Candidatos
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM candidates WHERE organization_id=$1`, orgID,
	).Scan(&c.Candidates); err != nil {
		return nil, err
	}

	// Vacantes por estado
	rows, err := h.pool.Query(ctx,
		`SELECT status, count(*) FROM vacancies WHERE organization_id=$1 GROUP BY status`, orgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var status string
		var cnt int
		if err := rows.Scan(&status, &cnt); err != nil {
			return nil, err
		}
		switch status {
		case "draft":
			c.Vacancies.Draft = cnt
		case "published":
			c.Vacancies.Published = cnt
		case "closed":
			c.Vacancies.Closed = cnt
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Compañías
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM companies WHERE organization_id=$1`, orgID,
	).Scan(&c.Companies); err != nil {
		return nil, err
	}

	// Reclutadores activos
	if err := h.pool.QueryRow(ctx,
		`SELECT count(*) FROM recruiters WHERE organization_id=$1 AND is_active=true`, orgID,
	).Scan(&c.Recruiters); err != nil {
		return nil, err
	}

	// Pipeline: postulaciones por estado en la org
	pipeRows, err := h.pool.Query(ctx, `
		SELECT a.status, count(*)
		FROM applications a
		JOIN vacancies v ON v.id = a.vacancy_id
		WHERE v.organization_id=$1
		GROUP BY a.status`, orgID)
	if err != nil {
		return nil, err
	}
	defer pipeRows.Close()
	for pipeRows.Next() {
		var status string
		var cnt int
		if err := pipeRows.Scan(&status, &cnt); err != nil {
			return nil, err
		}
		switch status {
		case "nueva":
			c.Pipeline.Nueva = cnt
		case "en_revision":
			c.Pipeline.EnRevision = cnt
		case "entrevista":
			c.Pipeline.Entrevista = cnt
		case "rechazada":
			c.Pipeline.Rechazada = cnt
		case "contratada":
			c.Pipeline.Contratada = cnt
		}
	}
	return &c, pipeRows.Err()
}
