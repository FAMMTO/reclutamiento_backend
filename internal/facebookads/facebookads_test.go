package facebookads

import (
	"context"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/platform/crypto"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/db"
)

// testKey es una clave fija de 32 bytes para tests (nunca usar en producción).
var testKey = []byte("jobbly-test-key-0000000000000000")

func setupFb(t *testing.T) (*pgxpool.Pool, uuid.UUID) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL no definida; omitiendo tests de integración")
	}
	ctx := context.Background()
	pool, err := db.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("conectando a DB de pruebas: %v", err)
	}
	t.Cleanup(pool.Close)

	if err := db.Migrate(ctx, pool, nil); err != nil {
		t.Fatalf("migrando: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		TRUNCATE facebook_ad_drafts, facebook_ads_configs,
		         applications, candidates, ruta_vacancies, rutas,
		         vacancies, companies, refresh_tokens, recruiters,
		         organizations CASCADE`); err != nil {
		t.Fatalf("limpiando: %v", err)
	}

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO organizations (name) VALUES ('TestOrg') RETURNING id`).Scan(&orgID); err != nil {
		t.Fatal(err)
	}
	return pool, orgID
}

func newSvc(t *testing.T, pool *pgxpool.Pool) *Service {
	t.Helper()
	enc, err := crypto.New(testKey)
	if err != nil {
		t.Fatal(err)
	}
	return NewService(pool, enc, "http://localhost:4000/callback", "http://localhost:5173")
}

func TestGetConfig_Default(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)

	cfg, err := svc.GetConfig(context.Background(), orgID)
	if err != nil {
		t.Fatalf("GetConfig vacía: %v", err)
	}
	if cfg.IsConnected {
		t.Error("config por defecto debe tener IsConnected=false")
	}
	if cfg.APIVersion != "v23.0" {
		t.Errorf("APIVersion por defecto = %q", cfg.APIVersion)
	}
	if cfg.AppSecretSet || cfg.AccessTokenSet {
		t.Error("sin config guardada no debe haber secretos")
	}
}

func TestSaveConfig_EncryptsSecrets(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)
	ctx := context.Background()

	// Primera vez: guarda credenciales
	cfg, err := svc.SaveConfig(ctx, orgID, SaveConfigInput{
		AppID:      "123456",
		AppSecret:  "super-secret",
		APIVersion: "v23.0",
	})
	if err != nil {
		t.Fatalf("SaveConfig: %v", err)
	}
	if !cfg.AppSecretSet {
		t.Error("AppSecretSet debería ser true")
	}
	if cfg.AccessTokenSet {
		t.Error("AccessTokenSet debería ser false (no se pasó token)")
	}

	// GetConfig no expone el texto plano
	cfg2, err := svc.GetConfig(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg2.AppSecretSet {
		t.Error("AppSecretSet debe persistir")
	}

	// Segunda SaveConfig con AppSecret vacío: conserva el cifrado previo
	cfg3, err := svc.SaveConfig(ctx, orgID, SaveConfigInput{
		AppID:      "123456",
		AppSecret:  "", // no cambiar
		APIVersion: "v23.0",
	})
	if err != nil {
		t.Fatalf("segunda SaveConfig: %v", err)
	}
	if !cfg3.AppSecretSet {
		t.Error("AppSecretSet no debe borrarse si no se pasa nuevo valor")
	}
}

func TestSaveConfig_WithAccessToken(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)
	ctx := context.Background()

	_, err := svc.SaveConfig(ctx, orgID, SaveConfigInput{
		AppID:       "app1",
		AppSecret:   "secret1",
		AccessToken: "EAAtoken",
		APIVersion:  "v23.0",
	})
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := svc.GetConfig(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.AppSecretSet || !cfg.AccessTokenSet {
		t.Errorf("ambos secretos deben estar guardados: secret=%v token=%v", cfg.AppSecretSet, cfg.AccessTokenSet)
	}
}

func TestListAds_Empty(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)

	ads, err := svc.ListAds(context.Background(), orgID)
	if err != nil {
		t.Fatalf("ListAds: %v", err)
	}
	if len(ads) != 0 {
		t.Errorf("esperaba 0 anuncios, hay %d", len(ads))
	}
}

func TestCreateAd_And_List(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)
	ctx := context.Background()

	draft, err := svc.CreateAd(ctx, orgID, CreateAdInput{
		CampaignName:     "Campaña Operaciones",
		Objective:        "OUTCOME_TRAFFIC",
		DailyBudgetCents: 5000,
		AdTitle:          "Únete al equipo",
		AdBody:           "Buscamos coordinadores en Monterrey.",
		LinkURL:          "https://jobbly.mx/vacante/123",
	})
	if err != nil {
		t.Fatalf("CreateAd: %v", err)
	}
	if draft.Status != "Borrador" {
		t.Errorf("status inicial = %q, esperaba Borrador", draft.Status)
	}

	ads, err := svc.ListAds(ctx, orgID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ads) != 1 {
		t.Fatalf("esperaba 1 anuncio, hay %d", len(ads))
	}
	if ads[0].CampaignName != "Campaña Operaciones" {
		t.Errorf("CampaignName = %q", ads[0].CampaignName)
	}
}

func TestSetAdStatus(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)
	ctx := context.Background()

	draft, _ := svc.CreateAd(ctx, orgID, CreateAdInput{
		CampaignName:     "Test",
		Objective:        "OUTCOME_LEADS",
		DailyBudgetCents: 1000,
		AdTitle:          "T",
		AdBody:           "B",
		LinkURL:          "https://jobbly.mx",
	})

	updated, err := svc.SetAdStatus(ctx, orgID, draft.ID, "Pausado")
	if err != nil {
		t.Fatalf("SetAdStatus: %v", err)
	}
	if updated.Status != "Pausado" {
		t.Errorf("status = %q, esperaba Pausado", updated.Status)
	}

	// estado inválido se rechaza
	if _, err := svc.SetAdStatus(ctx, orgID, draft.ID, "estadoFalso"); err == nil {
		t.Error("estado inválido debió rechazarse")
	}

	// anuncio de otra org → ErrAdNotFound
	otherOrg := uuid.New()
	if _, err := svc.SetAdStatus(ctx, otherOrg, draft.ID, "Pausado"); err == nil {
		t.Error("anuncio de otra org debió retornar error")
	}
}

func TestDeleteAd(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)
	ctx := context.Background()

	draft, _ := svc.CreateAd(ctx, orgID, CreateAdInput{
		CampaignName:     "Para borrar",
		Objective:        "OUTCOME_AWARENESS",
		DailyBudgetCents: 2000,
		AdTitle:          "Título",
		AdBody:           "Cuerpo",
		LinkURL:          "https://jobbly.mx",
	})

	if err := svc.DeleteAd(ctx, orgID, draft.ID); err != nil {
		t.Fatalf("DeleteAd: %v", err)
	}

	ads, _ := svc.ListAds(ctx, orgID)
	if len(ads) != 0 {
		t.Errorf("después de borrar debía quedar 0 anuncios, hay %d", len(ads))
	}

	// segundo delete → ErrAdNotFound
	if err := svc.DeleteAd(ctx, orgID, draft.ID); err == nil {
		t.Error("borrar anuncio ya eliminado debió retornar error")
	}
}

func TestSetSelection(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)
	ctx := context.Background()

	// Sin config previo SetSelection falla
	if _, err := svc.SetSelection(ctx, orgID, "act_111", "page_222"); err == nil {
		t.Error("SetSelection sin config previa debió fallar")
	}

	// Crear config primero
	if _, err := svc.SaveConfig(ctx, orgID, SaveConfigInput{
		AppID: "myapp", AppSecret: "s", APIVersion: "v23.0",
	}); err != nil {
		t.Fatal(err)
	}

	cfg, err := svc.SetSelection(ctx, orgID, "act_999", "page_888")
	if err != nil {
		t.Fatalf("SetSelection: %v", err)
	}
	if cfg.AdAccountID != "act_999" || cfg.PageID != "page_888" {
		t.Errorf("selección = %q / %q", cfg.AdAccountID, cfg.PageID)
	}
}

func TestDisconnect(t *testing.T) {
	pool, orgID := setupFb(t)
	svc := newSvc(t, pool)
	ctx := context.Background()

	// Crear config con token
	if _, err := svc.SaveConfig(ctx, orgID, SaveConfigInput{
		AppID: "app", AppSecret: "s", AccessToken: "tok", APIVersion: "v23.0",
	}); err != nil {
		t.Fatal(err)
	}

	// Marcar como conectado directamente en DB para simular OAuth completo
	if _, err := pool.Exec(ctx,
		`UPDATE facebook_ads_configs SET is_connected=true WHERE organization_id=$1`, orgID); err != nil {
		t.Fatal(err)
	}

	if err := svc.Disconnect(ctx, orgID); err != nil {
		t.Fatalf("Disconnect: %v", err)
	}

	cfg, _ := svc.GetConfig(ctx, orgID)
	if cfg.IsConnected {
		t.Error("después de Disconnect, IsConnected debe ser false")
	}
	if cfg.AccessTokenSet {
		t.Error("después de Disconnect, AccessTokenSet debe ser false")
	}
}
