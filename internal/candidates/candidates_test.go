package candidates

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/auth"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/audit"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/db"
	"github.com/FAMMTO/reclutamiento_backend/internal/vacancies"
)

// Test de integración del flujo completo Fase 2 + Fase 3:
// crear vacante → publicarla → postulación pública → pipeline de estados.
// Requiere TEST_DATABASE_URL (la base se trunca).
func setup(t *testing.T) (*pgxpool.Pool, auth.Identity) {
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
		t.Fatalf("migrando: %v", err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE audit_log, applications, candidates,
		ruta_vacancies, rutas, vacancies, companies, refresh_tokens, recruiters,
		organizations CASCADE`); err != nil {
		t.Fatalf("limpiando: %v", err)
	}

	var orgID, recruiterID uuid.UUID
	err = pool.QueryRow(ctx, `
		WITH org AS (INSERT INTO organizations (name) VALUES ('Test') RETURNING id)
		INSERT INTO recruiters (organization_id, name, email, password_hash, permission, password_changed)
		SELECT id, 'Admin', 'admin@test.mx', 'x', 'Administrador', true FROM org
		RETURNING organization_id, id`).Scan(&orgID, &recruiterID)
	if err != nil {
		t.Fatal(err)
	}
	return pool, auth.Identity{
		RecruiterID:     recruiterID,
		OrganizationID:  orgID,
		Role:            "Administrador",
		PasswordChanged: true,
	}
}

func auditLogger(pool *pgxpool.Pool) *audit.Logger {
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	return audit.New(pool, log)
}

func TestFullVacancyAndApplicationFlow(t *testing.T) {
	pool, actor := setup(t)
	ctx := context.Background()
	vacancyService := vacancies.NewService(pool, auditLogger(pool))
	candidateService := NewService(pool, auditLogger(pool))

	// 1. crear vacante publicada (find-or-create de la compañía por nombre)
	vacancy, err := vacancyService.Create(ctx, actor, vacancies.CreateInput{
		SurveyName:     "Evaluación operaciones",
		JobTitle:       "Coordinador de operaciones",
		JobDescription: "Coordinar la operación diaria.",
		Company:        "ACME de México",
		State:          "Nuevo León",
		Municipality:   "Monterrey",
		WorkMode:       "Presencial",
		RequestedSex:   "Ambos",
		Activities:     []string{"Coordinar equipo", "Reportar avances"},
		CustomBoxes:    []string{"¿Por qué te interesa el puesto?"},
		Publish:        true,
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("crear vacante: %v", err)
	}
	if vacancy.Status != "published" || vacancy.PublishedAt == nil {
		t.Fatalf("la vacante debió quedar publicada: %+v", vacancy)
	}
	if vacancy.CompanyName != "ACME de México" {
		t.Errorf("company = %q", vacancy.CompanyName)
	}

	// 2. la vista pública la muestra
	publicList, err := vacancyService.PublicList(ctx)
	if err != nil || len(publicList) != 1 {
		t.Fatalf("PublicList: %v (%d resultados)", err, len(publicList))
	}

	// 3. postulación pública válida
	age := 29
	_, err = candidateService.Apply(ctx, ApplyInput{
		VacancyID: vacancy.ID,
		Phone:     "+52 81 1234 5678",
		Name:      "María Pérez",
		Age:       &age,
		Email:     "maria@example.com",
		State:     "Nuevo León",
		Education: "Universidad",
		Answers:   []Answer{{Question: "¿Por qué te interesa el puesto?", Answer: "Crecimiento."}},
	}, "127.0.0.1")
	if err != nil {
		t.Fatalf("postulación válida falló: %v", err)
	}

	// 4. postulación duplicada a la misma vacante se rechaza
	_, err = candidateService.Apply(ctx, ApplyInput{
		VacancyID: vacancy.ID,
		Phone:     "+52 81 1234 5678",
		Name:      "María Pérez",
		Education: "Universidad",
	}, "127.0.0.1")
	if !errors.Is(err, ErrAlreadyApplied) {
		t.Fatalf("esperaba ErrAlreadyApplied, fue %v", err)
	}

	// 5. el candidato aparece para el reclutador con su postulación
	candidatesList, total, err := candidateService.ListCandidates(ctx, actor.OrganizationID, "", 1, 20)
	if err != nil || total != 1 {
		t.Fatalf("ListCandidates: %v (total %d)", err, total)
	}
	if candidatesList[0].Name != "María Pérez" {
		t.Errorf("candidato = %+v", candidatesList[0])
	}
	apps, err := candidateService.ListApplications(ctx, actor.OrganizationID, &vacancy.ID)
	if err != nil || len(apps) != 1 {
		t.Fatalf("ListApplications: %v (%d)", err, len(apps))
	}
	if apps[0].Status != "nueva" || len(apps[0].Answers) != 1 {
		t.Errorf("application = %+v", apps[0])
	}

	// 6. pipeline de estados
	if err := candidateService.SetApplicationStatus(ctx, actor, apps[0].ID, "entrevista", "127.0.0.1"); err != nil {
		t.Fatalf("SetApplicationStatus: %v", err)
	}
	if err := candidateService.SetApplicationStatus(ctx, actor, apps[0].ID, "estado-falso", "127.0.0.1"); !errors.Is(err, ErrInvalidStatus) {
		t.Fatalf("estado inválido debió rechazarse, fue %v", err)
	}

	// 7. cerrar la vacante la saca de la vista pública y bloquea postulaciones
	if err := vacancyService.SetStatus(ctx, actor, vacancy.ID, "closed", "127.0.0.1"); err != nil {
		t.Fatalf("cerrar vacante: %v", err)
	}
	publicList, _ = vacancyService.PublicList(ctx)
	if len(publicList) != 0 {
		t.Fatal("una vacante cerrada no debe listarse públicamente")
	}
	_, err = candidateService.Apply(ctx, ApplyInput{
		VacancyID: vacancy.ID,
		Phone:     "+52 55 0000 1111",
		Name:      "Otro Candidato",
		Education: "Preparatoria",
	}, "127.0.0.1")
	if !errors.Is(err, ErrVacancyNotOpen) {
		t.Fatalf("postular a vacante cerrada debió fallar con ErrVacancyNotOpen, fue %v", err)
	}

	// 8. transición inválida draft←closed directa a draft
	if err := vacancyService.SetStatus(ctx, actor, vacancy.ID, "draft", "127.0.0.1"); !errors.Is(err, vacancies.ErrBadStatus) {
		t.Fatalf("closed→draft debió rechazarse, fue %v", err)
	}

	// 9. upsert del candidato: nueva vacante, mismo teléfono actualiza perfil
	second, err := vacancyService.Create(ctx, actor, vacancies.CreateInput{
		SurveyName:     "Vacante 2",
		JobTitle:       "Analista",
		JobDescription: "Analizar.",
		Company:        "ACME de México",
		WorkMode:       "Remoto",
		RequestedSex:   "Ambos",
		Activities:     []string{"Analizar datos"},
		Publish:        true,
	}, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	newAge := 30
	if _, err = candidateService.Apply(ctx, ApplyInput{
		VacancyID: second.ID,
		Phone:     "+52 81 1234 5678",
		Name:      "María Pérez García",
		Age:       &newAge,
		Education: "Universidad",
		Degree:    "Ing. Industrial",
	}, "127.0.0.1"); err != nil {
		t.Fatalf("segunda postulación: %v", err)
	}
	candidatesList, total, _ = candidateService.ListCandidates(ctx, actor.OrganizationID, "maría", 1, 20)
	if total != 1 {
		t.Fatalf("el candidato debió hacer upsert, no duplicarse (total %d)", total)
	}
	if candidatesList[0].Name != "María Pérez García" || candidatesList[0].Degree != "Ing. Industrial" {
		t.Errorf("el perfil no se actualizó: %+v", candidatesList[0])
	}
}

func TestApplyValidation(t *testing.T) {
	pool, _ := setup(t)
	service := NewService(pool, auditLogger(pool))
	ctx := context.Background()

	cases := []ApplyInput{
		{VacancyID: uuid.New(), Phone: "abc", Name: "Juan Pérez", Education: "x"}, // teléfono inválido
		{VacancyID: uuid.New(), Phone: "5512345678", Name: "X1", Education: "x"},  // nombre inválido
		{VacancyID: uuid.New(), Phone: "5512345678", Name: "Juan Pérez"},          // sin educación
	}
	for i, input := range cases {
		if _, err := service.Apply(ctx, input, "127.0.0.1"); !errors.Is(err, ErrValidation) {
			t.Errorf("caso %d debió fallar con ErrValidation, fue %v", i, err)
		}
	}

	// vacante inexistente
	if _, err := service.Apply(ctx, ApplyInput{
		VacancyID: uuid.New(), Phone: "5512345678", Name: "Juan Pérez", Education: "Universidad",
	}, "127.0.0.1"); !errors.Is(err, ErrVacancyNotOpen) {
		t.Errorf("vacante inexistente: esperaba ErrVacancyNotOpen, fue %v", err)
	}
}
