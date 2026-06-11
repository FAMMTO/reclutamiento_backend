// Package candidates gestiona candidatos y postulaciones. La postulación
// entra por el endpoint público (InfoMatch + respuestas de la vacante);
// el candidato se identifica por teléfono dentro de la organización y se
// hace upsert de su perfil con cada postulación.
package candidates

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strconv"
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

var (
	phoneRe = regexp.MustCompile(`^[0-9+ ]{7,20}$`)
	nameRe  = regexp.MustCompile(`^[\p{L}][\p{L} ]{1,119}$`)
	emailRe = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

	ErrValidation       = errors.New("validación")
	ErrAlreadyApplied   = errors.New("ya te postulaste a esta vacante")
	ErrVacancyNotOpen   = errors.New("la vacante no está disponible")
	ErrInvalidStatus    = errors.New("estado de postulación inválido")
	applicationStatuses = map[string]bool{"nueva": true, "en_revision": true, "entrevista": true, "rechazada": true, "contratada": true}
)

type Candidate struct {
	ID             uuid.UUID `json:"id"`
	Phone          string    `json:"phone"`
	Name           string    `json:"name"`
	Age            *int      `json:"age"`
	Email          string    `json:"email"`
	State          string    `json:"state"`
	Municipality   string    `json:"municipality"`
	Education      string    `json:"education"`
	Degree         string    `json:"degree"`
	Certifications []string  `json:"certifications"`
	DesiredSalary  string    `json:"desiredSalary"`
	CreatedAt      time.Time `json:"createdAt"`
}

type Answer struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type Application struct {
	ID        uuid.UUID `json:"id"`
	Candidate Candidate `json:"candidate"`
	VacancyID uuid.UUID `json:"vacancyId"`
	JobTitle  string    `json:"jobTitle"`
	Answers   []Answer  `json:"answers"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type Service struct {
	pool  *pgxpool.Pool
	audit *audit.Logger
}

func NewService(pool *pgxpool.Pool, auditLog *audit.Logger) *Service {
	return &Service{pool: pool, audit: auditLog}
}

// ---- Postulación pública ----

type ApplyInput struct {
	VacancyID     uuid.UUID `json:"vacancyId"`
	Phone         string    `json:"phone"`
	Name          string    `json:"name"`
	Age           *int      `json:"age"`
	Email         string    `json:"email"`
	State         string    `json:"state"`
	Municipality  string    `json:"municipality"`
	Education     string    `json:"education"`
	Degree        string    `json:"degree"`
	DesiredSalary string    `json:"desiredSalary"`
	Answers       []Answer  `json:"answers"`
}

func (input *ApplyInput) validate() error {
	input.Phone = strings.TrimSpace(input.Phone)
	input.Name = strings.TrimSpace(input.Name)
	input.Email = strings.TrimSpace(strings.ToLower(input.Email))
	input.State = strings.TrimSpace(input.State)
	input.Municipality = strings.TrimSpace(input.Municipality)
	input.Education = strings.TrimSpace(input.Education)
	input.Degree = strings.TrimSpace(input.Degree)
	input.DesiredSalary = strings.TrimSpace(input.DesiredSalary)

	switch {
	case input.VacancyID == uuid.Nil:
		return errors.New("vacancyId es obligatorio")
	case !phoneRe.MatchString(input.Phone):
		return errors.New("teléfono inválido: solo dígitos, + y espacios (7-20 caracteres)")
	case !nameRe.MatchString(input.Name):
		return errors.New("nombre inválido: solo letras y espacios")
	case input.Age != nil && (*input.Age < 14 || *input.Age > 99):
		return errors.New("edad fuera de rango")
	case input.Email != "" && !emailRe.MatchString(input.Email):
		return errors.New("email inválido")
	case input.Education == "":
		return errors.New("education es obligatoria")
	case len(input.Answers) > 50:
		return errors.New("demasiadas respuestas")
	}
	for i := range input.Answers {
		input.Answers[i].Question = strings.TrimSpace(input.Answers[i].Question)
		input.Answers[i].Answer = strings.TrimSpace(input.Answers[i].Answer)
		if len(input.Answers[i].Answer) > 4000 {
			return errors.New("una respuesta excede los 4,000 caracteres")
		}
	}
	return nil
}

// Apply registra la postulación pública: upsert del candidato por
// (organización, teléfono) y alta de la application (única por vacante).
func (s *Service) Apply(ctx context.Context, input ApplyInput, ip string) (*uuid.UUID, error) {
	if err := input.validate(); err != nil {
		return nil, errors.Join(ErrValidation, err)
	}

	var orgID uuid.UUID
	var status string
	err := s.pool.QueryRow(ctx,
		`SELECT organization_id, status FROM vacancies WHERE id = $1`,
		input.VacancyID).Scan(&orgID, &status)
	if err != nil || status != "published" {
		return nil, ErrVacancyNotOpen
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// upsert del candidato: la postulación más reciente actualiza el perfil
	var candidateID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO candidates (organization_id, phone, name, age, email, state,
		                        municipality, education, degree, desired_salary)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (organization_id, phone) DO UPDATE SET
			name = EXCLUDED.name,
			age = coalesce(EXCLUDED.age, candidates.age),
			email = CASE WHEN EXCLUDED.email <> '' THEN EXCLUDED.email ELSE candidates.email END,
			state = CASE WHEN EXCLUDED.state <> '' THEN EXCLUDED.state ELSE candidates.state END,
			municipality = CASE WHEN EXCLUDED.municipality <> '' THEN EXCLUDED.municipality ELSE candidates.municipality END,
			education = EXCLUDED.education,
			degree = CASE WHEN EXCLUDED.degree <> '' THEN EXCLUDED.degree ELSE candidates.degree END,
			desired_salary = CASE WHEN EXCLUDED.desired_salary <> '' THEN EXCLUDED.desired_salary ELSE candidates.desired_salary END,
			updated_at = now()
		RETURNING id`,
		orgID, input.Phone, input.Name, input.Age, input.Email, input.State,
		input.Municipality, input.Education, input.Degree, input.DesiredSalary,
	).Scan(&candidateID)
	if err != nil {
		return nil, err
	}

	answers, _ := json.Marshal(input.Answers)
	var applicationID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO applications (organization_id, candidate_id, vacancy_id, answers)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (candidate_id, vacancy_id) DO NOTHING
		RETURNING id`,
		orgID, candidateID, input.VacancyID, answers).Scan(&applicationID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAlreadyApplied
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}

	s.audit.Record(ctx, audit.Entry{Action: "applications.create",
		Entity: "application", EntityID: applicationID.String(),
		Detail: map[string]any{"vacancyId": input.VacancyID, "candidateId": candidateID}, IP: ip})
	return &applicationID, nil
}

// ---- Lado reclutador ----

func (s *Service) ListCandidates(ctx context.Context, orgID uuid.UUID, search string, page, pageSize int) ([]Candidate, int, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	where := "organization_id = $1"
	args := []any{orgID}
	if search != "" {
		args = append(args, "%"+search+"%")
		idx := strconv.Itoa(len(args))
		where += " AND (name ILIKE $" + idx + " OR phone ILIKE $" + idx + " OR email ILIKE $" + idx + ")"
	}

	var total int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM candidates WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	args = append(args, pageSize, (page-1)*pageSize)
	rows, err := s.pool.Query(ctx, `
		SELECT id, phone, name, age, email, state, municipality, education,
		       degree, certifications, desired_salary, created_at
		  FROM candidates WHERE `+where+`
		 ORDER BY created_at DESC
		 LIMIT $`+strconv.Itoa(len(args)-1)+` OFFSET $`+strconv.Itoa(len(args)), args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	result := []Candidate{}
	for rows.Next() {
		var c Candidate
		if err := rows.Scan(&c.ID, &c.Phone, &c.Name, &c.Age, &c.Email, &c.State,
			&c.Municipality, &c.Education, &c.Degree, &c.Certifications,
			&c.DesiredSalary, &c.CreatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, c)
	}
	return result, total, rows.Err()
}

// ListApplications devuelve postulaciones de la organización; opcionalmente
// filtradas por vacante.
func (s *Service) ListApplications(ctx context.Context, orgID uuid.UUID, vacancyID *uuid.UUID) ([]Application, error) {
	where := "a.organization_id = $1"
	args := []any{orgID}
	if vacancyID != nil {
		args = append(args, *vacancyID)
		where += " AND a.vacancy_id = $2"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, a.vacancy_id, v.job_title, a.answers, a.status, a.created_at,
		       c.id, c.phone, c.name, c.age, c.email, c.state, c.municipality,
		       c.education, c.degree, c.certifications, c.desired_salary, c.created_at
		  FROM applications a
		  JOIN candidates c ON c.id = a.candidate_id
		  JOIN vacancies v ON v.id = a.vacancy_id
		 WHERE `+where+`
		 ORDER BY a.created_at DESC
		 LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := []Application{}
	for rows.Next() {
		var app Application
		var answers []byte
		if err := rows.Scan(&app.ID, &app.VacancyID, &app.JobTitle, &answers,
			&app.Status, &app.CreatedAt,
			&app.Candidate.ID, &app.Candidate.Phone, &app.Candidate.Name,
			&app.Candidate.Age, &app.Candidate.Email, &app.Candidate.State,
			&app.Candidate.Municipality, &app.Candidate.Education,
			&app.Candidate.Degree, &app.Candidate.Certifications,
			&app.Candidate.DesiredSalary, &app.Candidate.CreatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(answers, &app.Answers); err != nil {
			app.Answers = []Answer{}
		}
		result = append(result, app)
	}
	return result, rows.Err()
}

func (s *Service) SetApplicationStatus(ctx context.Context, actor auth.Identity, id uuid.UUID, status, ip string) error {
	if !applicationStatuses[status] {
		return ErrInvalidStatus
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE applications SET status = $1, updated_at = now()
		 WHERE id = $2 AND organization_id = $3`,
		status, id, actor.OrganizationID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	s.audit.Record(ctx, audit.Entry{ActorID: &actor.RecruiterID, Action: "applications.set_status",
		Entity: "application", EntityID: id.String(),
		Detail: map[string]any{"status": status}, IP: ip})
	return nil
}

// ---- Handlers ----

type Handlers struct{ service *Service }

func NewHandlers(service *Service) *Handlers { return &Handlers{service: service} }

// Apply maneja POST /api/v1/public/applications (sin auth, rate-limited).
func (h *Handlers) Apply(w http.ResponseWriter, r *http.Request) {
	var input ApplyInput
	if err := web.DecodeJSON(w, r, &input); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	id, err := h.service.Apply(r.Context(), input, httpserver.ClientIP(r))
	switch {
	case errors.Is(err, ErrValidation):
		web.RespondError(w, http.StatusBadRequest, "validation",
			strings.TrimPrefix(err.Error(), ErrValidation.Error()+"\n"))
	case errors.Is(err, ErrVacancyNotOpen):
		web.RespondError(w, http.StatusNotFound, "vacancy_not_open", err.Error())
	case errors.Is(err, ErrAlreadyApplied):
		web.RespondError(w, http.StatusConflict, "already_applied", err.Error())
	case err != nil:
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
	default:
		web.RespondJSON(w, http.StatusCreated, map[string]any{"applicationId": id})
	}
}

func (h *Handlers) ListCandidates(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	query := r.URL.Query()
	page, _ := strconv.Atoi(query.Get("page"))
	pageSize, _ := strconv.Atoi(query.Get("pageSize"))
	list, total, err := h.service.ListCandidates(r.Context(), identity.OrganizationID,
		strings.TrimSpace(query.Get("search")), page, pageSize)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]any{"candidates": list, "total": total})
}

func (h *Handlers) ListApplications(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	var vacancyID *uuid.UUID
	if raw := r.URL.Query().Get("vacancyId"); raw != "" {
		id, err := uuid.Parse(raw)
		if err != nil {
			web.RespondError(w, http.StatusBadRequest, "bad_request", "vacancyId inválido")
			return
		}
		vacancyID = &id
	}
	list, err := h.service.ListApplications(r.Context(), identity.OrganizationID, vacancyID)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string][]Application{"applications": list})
}

func (h *Handlers) SetApplicationStatus(w http.ResponseWriter, r *http.Request) {
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
	err = h.service.SetApplicationStatus(r.Context(), identity, id, body.Status, httpserver.ClientIP(r))
	switch {
	case errors.Is(err, ErrInvalidStatus):
		web.RespondError(w, http.StatusBadRequest, "bad_status", err.Error())
	case errors.Is(err, pgx.ErrNoRows):
		web.RespondError(w, http.StatusNotFound, "not_found", "Postulación no encontrada")
	case err != nil:
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
	default:
		web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
	}
}
