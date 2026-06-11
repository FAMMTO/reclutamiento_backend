// Package vacancies implementa el ciclo de vida de vacantes
// (draft → published → closed), su CRUD org-scoped y la vista pública
// que consumen los candidatos.
package vacancies

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/auth"
	"github.com/FAMMTO/reclutamiento_backend/internal/companies"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/audit"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/httpserver"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/web"
)

type Vacancy struct {
	ID              uuid.UUID  `json:"id"`
	CompanyID       uuid.UUID  `json:"companyId"`
	CompanyName     string     `json:"company"`
	SurveyName      string     `json:"surveyName"`
	JobTitle        string     `json:"jobTitle"`
	JobDescription  string     `json:"jobDescription"`
	State           string     `json:"state"`
	Municipality    string     `json:"municipality"`
	Location        string     `json:"location"`
	WorkMode        string     `json:"workMode"`
	SalaryRange     string     `json:"salaryRange"`
	Schedule        string     `json:"schedule"`
	RequestedSex    string     `json:"requestedSex"`
	EducationLevels []string   `json:"educationLevel"`
	Activities      []string   `json:"activities"`
	CustomBoxes     []string   `json:"customBoxes"`
	Status          string     `json:"status"`
	PublishedAt     *time.Time `json:"publishedAt"`
	CreatedAt       time.Time  `json:"createdAt"`
}

var (
	ErrValidation = errors.New("validación")
	ErrBadStatus  = errors.New("transición de estado inválida")
)

type Service struct {
	pool  *pgxpool.Pool
	audit *audit.Logger
}

func NewService(pool *pgxpool.Pool, auditLog *audit.Logger) *Service {
	return &Service{pool: pool, audit: auditLog}
}

const vacancyColumns = `v.id, v.company_id, c.name, v.survey_name, v.job_title,
	v.job_description, v.state, v.municipality, v.work_mode, v.salary_range,
	v.schedule, v.requested_sex, v.education_levels, v.activities, v.custom_boxes,
	v.status, v.published_at, v.created_at`

func scanVacancy(row pgx.Row) (Vacancy, error) {
	var v Vacancy
	var activities, customBoxes []byte
	err := row.Scan(&v.ID, &v.CompanyID, &v.CompanyName, &v.SurveyName, &v.JobTitle,
		&v.JobDescription, &v.State, &v.Municipality, &v.WorkMode, &v.SalaryRange,
		&v.Schedule, &v.RequestedSex, &v.EducationLevels, &activities, &customBoxes,
		&v.Status, &v.PublishedAt, &v.CreatedAt)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(activities, &v.Activities); err != nil {
		v.Activities = []string{}
	}
	if err := json.Unmarshal(customBoxes, &v.CustomBoxes); err != nil {
		v.CustomBoxes = []string{}
	}
	if v.Municipality != "" && v.State != "" {
		v.Location = v.Municipality + ", " + v.State
	} else {
		v.Location = v.State
	}
	return v, nil
}

type CreateInput struct {
	SurveyName      string   `json:"surveyName"`
	JobTitle        string   `json:"jobTitle"`
	JobDescription  string   `json:"jobDescription"`
	Company         string   `json:"company"`
	State           string   `json:"state"`
	Municipality    string   `json:"municipality"`
	WorkMode        string   `json:"workMode"`
	SalaryRange     string   `json:"salaryRange"`
	Schedule        string   `json:"schedule"`
	RequestedSex    string   `json:"requestedSex"`
	EducationLevels []string `json:"educationLevel"`
	Activities      []string `json:"activities"`
	CustomBoxes     []string `json:"customBoxes"`
	Publish         bool     `json:"publish"`
}

func cleanList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func (input *CreateInput) validate() error {
	input.SurveyName = strings.TrimSpace(input.SurveyName)
	input.JobTitle = strings.TrimSpace(input.JobTitle)
	input.JobDescription = strings.TrimSpace(input.JobDescription)
	input.Company = strings.TrimSpace(input.Company)
	input.State = strings.TrimSpace(input.State)
	input.Municipality = strings.TrimSpace(input.Municipality)
	input.Activities = cleanList(input.Activities)
	input.CustomBoxes = cleanList(input.CustomBoxes)
	input.EducationLevels = cleanList(input.EducationLevels)

	switch {
	case input.SurveyName == "" || len(input.SurveyName) > 200:
		return errors.New("surveyName es obligatorio (máx. 200 caracteres)")
	case input.JobTitle == "" || len(input.JobTitle) > 200:
		return errors.New("jobTitle es obligatorio (máx. 200 caracteres)")
	case input.JobDescription == "":
		return errors.New("jobDescription es obligatoria")
	case input.Company == "":
		return errors.New("company es obligatoria")
	case input.WorkMode != "Hibrida" && input.WorkMode != "Presencial" && input.WorkMode != "Remoto":
		return errors.New(`workMode debe ser "Hibrida", "Presencial" o "Remoto"`)
	case input.RequestedSex != "Hombre" && input.RequestedSex != "Mujer" && input.RequestedSex != "Ambos":
		return errors.New(`requestedSex debe ser "Hombre", "Mujer" o "Ambos"`)
	case len(input.Activities) == 0:
		return errors.New("se requiere al menos una actividad")
	}
	return nil
}

func (s *Service) Create(ctx context.Context, actor auth.Identity, input CreateInput, ip string) (*Vacancy, error) {
	if err := input.validate(); err != nil {
		return nil, errors.Join(ErrValidation, err)
	}
	companyID, err := companies.FindOrCreate(ctx, s.pool, actor.OrganizationID, input.Company, actor.RecruiterID)
	if err != nil {
		return nil, errors.Join(ErrValidation, err)
	}

	status := "draft"
	var publishedAt *time.Time
	if input.Publish {
		status = "published"
		now := time.Now()
		publishedAt = &now
	}
	activities, _ := json.Marshal(input.Activities)
	customBoxes, _ := json.Marshal(input.CustomBoxes)

	var id uuid.UUID
	err = s.pool.QueryRow(ctx, `
		INSERT INTO vacancies (organization_id, company_id, survey_name, job_title,
			job_description, state, municipality, work_mode, salary_range, schedule,
			requested_sex, education_levels, activities, custom_boxes, status,
			published_at, created_by)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING id`,
		actor.OrganizationID, companyID, input.SurveyName, input.JobTitle,
		input.JobDescription, input.State, input.Municipality, input.WorkMode,
		input.SalaryRange, input.Schedule, input.RequestedSex, input.EducationLevels,
		activities, customBoxes, status, publishedAt, actor.RecruiterID,
	).Scan(&id)
	if err != nil {
		return nil, err
	}

	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "vacancies.create",
		Entity: "vacancy", EntityID: id.String(),
		Detail: map[string]any{"jobTitle": input.JobTitle, "status": status}, IP: ip})

	vacancy, err := s.Get(ctx, actor.OrganizationID, id)
	if err != nil {
		return nil, err
	}
	return vacancy, nil
}

func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (*Vacancy, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+vacancyColumns+`
		  FROM vacancies v JOIN companies c ON c.id = v.company_id
		 WHERE v.id = $1 AND v.organization_id = $2`, id, orgID)
	vacancy, err := scanVacancy(row)
	if err != nil {
		return nil, err
	}
	return &vacancy, nil
}

type ListFilters struct {
	Status   string
	Search   string
	Page     int
	PageSize int
}

func (s *Service) List(ctx context.Context, orgID uuid.UUID, filters ListFilters) ([]Vacancy, int, error) {
	if filters.Page < 1 {
		filters.Page = 1
	}
	if filters.PageSize < 1 || filters.PageSize > 100 {
		filters.PageSize = 20
	}

	where := "v.organization_id = $1"
	args := []any{orgID}
	if filters.Status != "" {
		args = append(args, filters.Status)
		where += " AND v.status = $" + strconv.Itoa(len(args))
	}
	if filters.Search != "" {
		args = append(args, "%"+filters.Search+"%")
		idx := strconv.Itoa(len(args))
		where += " AND (v.job_title ILIKE $" + idx + " OR v.survey_name ILIKE $" + idx + " OR c.name ILIKE $" + idx + ")"
	}

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM vacancies v JOIN companies c ON c.id = v.company_id WHERE `+where,
		args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, filters.PageSize, (filters.Page-1)*filters.PageSize)
	rows, err := s.pool.Query(ctx, `
		SELECT `+vacancyColumns+`
		  FROM vacancies v JOIN companies c ON c.id = v.company_id
		 WHERE `+where+`
		 ORDER BY coalesce(v.published_at, v.created_at) DESC
		 LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)),
		args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	result := []Vacancy{}
	for rows.Next() {
		vacancy, err := scanVacancy(rows)
		if err != nil {
			return nil, 0, err
		}
		result = append(result, vacancy)
	}
	return result, total, rows.Err()
}

// validTransitions define el ciclo de vida: draft→published, published→closed,
// closed→published (reabrir) y draft→closed (descartar).
var validTransitions = map[string][]string{
	"draft":     {"published", "closed"},
	"published": {"closed"},
	"closed":    {"published"},
}

func (s *Service) SetStatus(ctx context.Context, actor auth.Identity, id uuid.UUID, next, ip string) error {
	var current string
	err := s.pool.QueryRow(ctx,
		`SELECT status FROM vacancies WHERE id = $1 AND organization_id = $2`,
		id, actor.OrganizationID).Scan(&current)
	if err != nil {
		return err
	}
	allowed := false
	for _, candidate := range validTransitions[current] {
		if candidate == next {
			allowed = true
			break
		}
	}
	if !allowed {
		return ErrBadStatus
	}

	if next == "published" {
		_, err = s.pool.Exec(ctx, `
			UPDATE vacancies SET status = 'published',
			       published_at = coalesce(published_at, now()), updated_at = now()
			 WHERE id = $1 AND organization_id = $2`, id, actor.OrganizationID)
	} else {
		_, err = s.pool.Exec(ctx, `
			UPDATE vacancies SET status = $1, updated_at = now()
			 WHERE id = $2 AND organization_id = $3`, next, id, actor.OrganizationID)
	}
	if err != nil {
		return err
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "vacancies.set_status",
		Entity: "vacancy", EntityID: id.String(),
		Detail: map[string]any{"from": current, "to": next}, IP: ip})
	return nil
}

// ---- Vista pública (sin auth) ----

// PublicGet devuelve una vacante solo si está publicada (cualquier organización).
func (s *Service) PublicGet(ctx context.Context, id uuid.UUID) (*Vacancy, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT `+vacancyColumns+`
		  FROM vacancies v JOIN companies c ON c.id = v.company_id
		 WHERE v.id = $1 AND v.status = 'published'`, id)
	vacancy, err := scanVacancy(row)
	if err != nil {
		return nil, err
	}
	return &vacancy, nil
}

func (s *Service) PublicList(ctx context.Context) ([]Vacancy, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+vacancyColumns+`
		  FROM vacancies v JOIN companies c ON c.id = v.company_id
		 WHERE v.status = 'published'
		 ORDER BY v.published_at DESC
		 LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []Vacancy{}
	for rows.Next() {
		vacancy, err := scanVacancy(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, vacancy)
	}
	return result, rows.Err()
}

// ---- Handlers ----

type Handlers struct{ service *Service }

func NewHandlers(service *Service) *Handlers { return &Handlers{service: service} }

func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	var input CreateInput
	if err := web.DecodeJSON(w, r, &input); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	vacancy, err := h.service.Create(r.Context(), identity, input, httpserver.ClientIP(r))
	if err != nil {
		if errors.Is(err, ErrValidation) {
			web.RespondError(w, http.StatusBadRequest, "validation",
				strings.TrimPrefix(err.Error(), ErrValidation.Error()+"\n"))
			return
		}
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusCreated, map[string]*Vacancy{"vacancy": vacancy})
}

func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	pageSize, _ := strconv.Atoi(query.Get("pageSize"))
	list, total, err := h.service.List(r.Context(), identity.OrganizationID, ListFilters{
		Status:   query.Get("status"),
		Search:   strings.TrimSpace(query.Get("search")),
		Page:     page,
		PageSize: pageSize,
	})
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]any{"vacancies": list, "total": total})
}

func (h *Handlers) Get(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "ID inválido")
		return
	}
	vacancy, err := h.service.Get(r.Context(), identity.OrganizationID, id)
	if err != nil {
		web.RespondError(w, http.StatusNotFound, "not_found", "Vacante no encontrada")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]*Vacancy{"vacancy": vacancy})
}

func (h *Handlers) SetStatus(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "ID inválido")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	err = h.service.SetStatus(r.Context(), identity, id, body.Status, httpserver.ClientIP(r))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		web.RespondError(w, http.StatusNotFound, "not_found", "Vacante no encontrada")
	case errors.Is(err, ErrBadStatus):
		web.RespondError(w, http.StatusBadRequest, "bad_status", "Transición de estado inválida")
	case err != nil:
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
	default:
		web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}

func (h *Handlers) PublicList(w http.ResponseWriter, r *http.Request) {
	list, err := h.service.PublicList(r.Context())
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string][]Vacancy{"vacancies": list})
}

func (h *Handlers) PublicGet(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "ID inválido")
		return
	}
	vacancy, err := h.service.PublicGet(r.Context(), id)
	if err != nil {
		web.RespondError(w, http.StatusNotFound, "not_found", "Vacante no disponible")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]*Vacancy{"vacancy": vacancy})
}
