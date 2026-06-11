// Package facebookads gestiona la integración con Meta Ads Graph API.
// Las credenciales (appSecret, accessToken) se cifran con AES-GCM antes de
// persistirse en Postgres. El frontend nunca recibe los valores en texto plano.
package facebookads

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FAMMTO/reclutamiento_backend/internal/platform/crypto"
)

// ── Tipos ─────────────────────────────────────────────────────────────────────

type Config struct {
	ID             uuid.UUID  `json:"id"`
	OrganizationID uuid.UUID  `json:"-"`
	AppID          string     `json:"appId"`
	AppSecretSet   bool       `json:"appSecretSet"`   // true = hay valor guardado; el plaintext nunca se expone
	AccessTokenSet bool       `json:"accessTokenSet"` // ídem
	AdAccountID    string     `json:"adAccountId"`
	PageID         string     `json:"pageId"`
	BusinessID     string     `json:"businessId"`
	APIVersion     string     `json:"apiVersion"`
	IsConnected    bool       `json:"isConnected"`
	ConnectedAt    *time.Time `json:"connectedAt"`
	UpdatedAt      time.Time  `json:"updatedAt"`
}

type SaveConfigInput struct {
	AppID       string `json:"appId"`
	AppSecret   string `json:"appSecret"`   // vacío = no cambiar
	AccessToken string `json:"accessToken"` // vacío = no cambiar
	AdAccountID string `json:"adAccountId"`
	PageID      string `json:"pageId"`
	BusinessID  string `json:"businessId"`
	APIVersion  string `json:"apiVersion"`
}

type AdDraft struct {
	ID               uuid.UUID  `json:"id"`
	OrganizationID   uuid.UUID  `json:"-"`
	VacancyID        *uuid.UUID `json:"vacancyId"`
	CampaignName     string     `json:"campaignName"`
	Objective        string     `json:"objective"`
	DailyBudgetCents int        `json:"dailyBudgetCents"`
	AdTitle          string     `json:"adTitle"`
	AdBody           string     `json:"adBody"`
	LinkURL          string     `json:"linkUrl"`
	Status           string     `json:"status"`
	CampaignID       *string    `json:"campaignId"`
	AdSetID          *string    `json:"adSetId"`
	AdID             *string    `json:"adId"`
	CreatedAt        time.Time  `json:"createdAt"`
}

type CreateAdInput struct {
	VacancyID        *uuid.UUID `json:"vacancyId"`
	CampaignName     string     `json:"campaignName"`
	Objective        string     `json:"objective"`
	DailyBudgetCents int        `json:"dailyBudgetCents"`
	AdTitle          string     `json:"adTitle"`
	AdBody           string     `json:"adBody"`
	LinkURL          string     `json:"linkUrl"`
}

var validObjectives = map[string]bool{
	"OUTCOME_TRAFFIC":    true,
	"OUTCOME_LEADS":      true,
	"OUTCOME_AWARENESS":  true,
	"OUTCOME_ENGAGEMENT": true,
}

var validAdStatuses = map[string]bool{
	"Borrador": true, "Publicado": true, "Pausado": true, "Error": true,
}

var (
	ErrConfigNotFound = errors.New("facebookads: configuración no encontrada")
	ErrAdNotFound     = errors.New("facebookads: anuncio no encontrado")
)

// graphBaseURL puede ser sobreescrito en tests.
var graphBaseURL = "https://graph.facebook.com"

// ── Tipos de discovery ────────────────────────────────────────────────────────

type AdAccount struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status int    `json:"status"`
}

type FbPage struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ── Servicio ──────────────────────────────────────────────────────────────────

type oauthStateEntry struct {
	orgID     uuid.UUID
	expiresAt time.Time
}

type Service struct {
	pool        *pgxpool.Pool
	enc         *crypto.Encryptor
	redirectURI string
	frontendURL string
	mu          sync.Mutex
	oauthStates map[string]oauthStateEntry
}

func NewService(pool *pgxpool.Pool, enc *crypto.Encryptor, redirectURI, frontendURL string) *Service {
	return &Service{
		pool:        pool,
		enc:         enc,
		redirectURI: redirectURI,
		frontendURL: frontendURL,
		oauthStates: make(map[string]oauthStateEntry),
	}
}

// ── Config ────────────────────────────────────────────────────────────────────

// GetConfig devuelve la configuración de la organización sin exponer secretos.
// Si no existe aún, devuelve una Config vacía con IsConnected=false.
func (s *Service) GetConfig(ctx context.Context, orgID uuid.UUID) (*Config, error) {
	row := s.pool.QueryRow(ctx, `
		SELECT id, app_id, app_secret_enc, access_token_enc,
		       ad_account_id, page_id, business_id, api_version,
		       is_connected, connected_at, updated_at
		FROM facebook_ads_configs
		WHERE organization_id = $1`, orgID)

	var cfg Config
	cfg.OrganizationID = orgID
	var appSecretEnc, accessTokenEnc string

	err := row.Scan(
		&cfg.ID, &cfg.AppID, &appSecretEnc, &accessTokenEnc,
		&cfg.AdAccountID, &cfg.PageID, &cfg.BusinessID, &cfg.APIVersion,
		&cfg.IsConnected, &cfg.ConnectedAt, &cfg.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return &Config{APIVersion: "v23.0", OrganizationID: orgID}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("facebookads: leer config: %w", err)
	}

	cfg.AppSecretSet = appSecretEnc != ""
	cfg.AccessTokenSet = accessTokenEnc != ""
	return &cfg, nil
}

// SaveConfig upsert la config. Campos vacíos en AppSecret/AccessToken conservan el valor anterior.
func (s *Service) SaveConfig(ctx context.Context, orgID uuid.UUID, in SaveConfigInput) (*Config, error) {
	if in.APIVersion == "" {
		in.APIVersion = "v23.0"
	}

	// Leer valores cifrados actuales para preservar si no se sobreescriben.
	var currentSecretEnc, currentTokenEnc string
	_ = s.pool.QueryRow(ctx,
		`SELECT app_secret_enc, access_token_enc FROM facebook_ads_configs WHERE organization_id = $1`,
		orgID,
	).Scan(&currentSecretEnc, &currentTokenEnc)

	newSecretEnc := currentSecretEnc
	if in.AppSecret != "" {
		enc, err := s.enc.Encrypt([]byte(in.AppSecret))
		if err != nil {
			return nil, fmt.Errorf("facebookads: cifrar appSecret: %w", err)
		}
		newSecretEnc = enc
	}

	newTokenEnc := currentTokenEnc
	if in.AccessToken != "" {
		enc, err := s.enc.Encrypt([]byte(in.AccessToken))
		if err != nil {
			return nil, fmt.Errorf("facebookads: cifrar accessToken: %w", err)
		}
		newTokenEnc = enc
	}

	var cfg Config
	cfg.OrganizationID = orgID
	var appSecretEnc, accessTokenEnc string

	err := s.pool.QueryRow(ctx, `
		INSERT INTO facebook_ads_configs
		    (organization_id, app_id, app_secret_enc, access_token_enc,
		     ad_account_id, page_id, business_id, api_version, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,now())
		ON CONFLICT (organization_id) DO UPDATE SET
		    app_id           = EXCLUDED.app_id,
		    app_secret_enc   = EXCLUDED.app_secret_enc,
		    access_token_enc = EXCLUDED.access_token_enc,
		    ad_account_id    = EXCLUDED.ad_account_id,
		    page_id          = EXCLUDED.page_id,
		    business_id      = EXCLUDED.business_id,
		    api_version      = EXCLUDED.api_version,
		    is_connected      = false,
		    updated_at       = now()
		RETURNING id, app_id, app_secret_enc, access_token_enc,
		          ad_account_id, page_id, business_id, api_version,
		          is_connected, connected_at, updated_at`,
		orgID, in.AppID, newSecretEnc, newTokenEnc,
		in.AdAccountID, in.PageID, in.BusinessID, in.APIVersion,
	).Scan(
		&cfg.ID, &cfg.AppID, &appSecretEnc, &accessTokenEnc,
		&cfg.AdAccountID, &cfg.PageID, &cfg.BusinessID, &cfg.APIVersion,
		&cfg.IsConnected, &cfg.ConnectedAt, &cfg.UpdatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("facebookads: guardar config: %w", err)
	}

	cfg.AppSecretSet = appSecretEnc != ""
	cfg.AccessTokenSet = accessTokenEnc != ""
	return &cfg, nil
}

// TestConnection hace GET /act_{adAccountId}?fields=name,account_status desde el servidor.
// Actualiza is_connected y connected_at en DB según el resultado.
func (s *Service) TestConnection(ctx context.Context, orgID uuid.UUID) (string, error) {
	accessToken, adAccountID, apiVersion, err := s.loadCredentials(ctx, orgID)
	if err != nil {
		return "", err
	}

	accountID := normalizeAccountID(adAccountID)
	endpoint := fmt.Sprintf("%s/%s/%s?fields=name,account_status&access_token=%s",
		graphBaseURL, apiVersion, accountID, url.QueryEscape(accessToken))

	resp, err := httpGet(ctx, endpoint)
	if err != nil {
		s.setConnected(ctx, orgID, false)
		return "", fmt.Errorf("facebookads: llamada a Graph API: %w", err)
	}

	type accountResp struct {
		Name   string `json:"name"`
		Status int    `json:"account_status"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	var body accountResp
	if err := json.Unmarshal(resp, &body); err != nil {
		s.setConnected(ctx, orgID, false)
		return "", fmt.Errorf("facebookads: parsear respuesta: %w", err)
	}
	if body.Error != nil {
		s.setConnected(ctx, orgID, false)
		return "", fmt.Errorf("facebookads: Graph API: %s", body.Error.Message)
	}

	s.setConnected(ctx, orgID, true)
	return body.Name, nil
}

// ── Anuncios (borradores) ─────────────────────────────────────────────────────

func (s *Service) ListAds(ctx context.Context, orgID uuid.UUID) ([]AdDraft, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, vacancy_id, campaign_name, objective, daily_budget_cents,
		       ad_title, ad_body, link_url, status, campaign_id, ad_set_id, ad_id, created_at
		FROM facebook_ad_drafts
		WHERE organization_id = $1
		ORDER BY created_at DESC`, orgID)
	if err != nil {
		return nil, fmt.Errorf("facebookads: listar anuncios: %w", err)
	}
	defer rows.Close()

	var drafts []AdDraft
	for rows.Next() {
		var d AdDraft
		d.OrganizationID = orgID
		if err := rows.Scan(
			&d.ID, &d.VacancyID, &d.CampaignName, &d.Objective, &d.DailyBudgetCents,
			&d.AdTitle, &d.AdBody, &d.LinkURL, &d.Status,
			&d.CampaignID, &d.AdSetID, &d.AdID, &d.CreatedAt,
		); err != nil {
			return nil, err
		}
		drafts = append(drafts, d)
	}
	if drafts == nil {
		drafts = []AdDraft{}
	}
	return drafts, rows.Err()
}

// CreateAd guarda el borrador en DB y luego intenta publicarlo en Meta.
// Si Meta falla, el borrador queda con status "Error" pero no se devuelve error HTTP.
func (s *Service) CreateAd(ctx context.Context, orgID uuid.UUID, in CreateAdInput) (*AdDraft, error) {
	if err := validateCreateAdInput(in); err != nil {
		return nil, err
	}

	// Persistir borrador
	var draft AdDraft
	draft.OrganizationID = orgID
	err := s.pool.QueryRow(ctx, `
		INSERT INTO facebook_ad_drafts
		    (organization_id, vacancy_id, campaign_name, objective,
		     daily_budget_cents, ad_title, ad_body, link_url)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		RETURNING id, vacancy_id, campaign_name, objective, daily_budget_cents,
		          ad_title, ad_body, link_url, status, campaign_id, ad_set_id, ad_id, created_at`,
		orgID, in.VacancyID, in.CampaignName, in.Objective,
		in.DailyBudgetCents, in.AdTitle, in.AdBody, in.LinkURL,
	).Scan(
		&draft.ID, &draft.VacancyID, &draft.CampaignName, &draft.Objective, &draft.DailyBudgetCents,
		&draft.AdTitle, &draft.AdBody, &draft.LinkURL, &draft.Status,
		&draft.CampaignID, &draft.AdSetID, &draft.AdID, &draft.CreatedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("facebookads: crear borrador: %w", err)
	}

	// Intentar publicar en Meta en background — error de Graph API no falla el request
	go func() {
		bgCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.publishToMeta(bgCtx, orgID, &draft)
	}()

	return &draft, nil
}

func (s *Service) SetAdStatus(ctx context.Context, orgID, adID uuid.UUID, status string) (*AdDraft, error) {
	if !validAdStatuses[status] {
		return nil, fmt.Errorf("facebookads: estado inválido %q", status)
	}

	var draft AdDraft
	draft.OrganizationID = orgID
	err := s.pool.QueryRow(ctx, `
		UPDATE facebook_ad_drafts SET status = $1
		WHERE id = $2 AND organization_id = $3
		RETURNING id, vacancy_id, campaign_name, objective, daily_budget_cents,
		          ad_title, ad_body, link_url, status, campaign_id, ad_set_id, ad_id, created_at`,
		status, adID, orgID,
	).Scan(
		&draft.ID, &draft.VacancyID, &draft.CampaignName, &draft.Objective, &draft.DailyBudgetCents,
		&draft.AdTitle, &draft.AdBody, &draft.LinkURL, &draft.Status,
		&draft.CampaignID, &draft.AdSetID, &draft.AdID, &draft.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrAdNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("facebookads: actualizar estado: %w", err)
	}
	return &draft, nil
}

func (s *Service) DeleteAd(ctx context.Context, orgID, adID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM facebook_ad_drafts WHERE id = $1 AND organization_id = $2`,
		adID, orgID)
	if err != nil {
		return fmt.Errorf("facebookads: eliminar anuncio: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrAdNotFound
	}
	return nil
}

// ── OAuth ─────────────────────────────────────────────────────────────────────

// GenerateOAuthURL construye la URL de autorización de Meta y guarda el state
// CSRF en memoria (expira en 10 minutos).
func (s *Service) GenerateOAuthURL(ctx context.Context, orgID uuid.UUID) (string, error) {
	var appID, apiVersion string
	err := s.pool.QueryRow(ctx,
		`SELECT app_id, api_version FROM facebook_ads_configs WHERE organization_id = $1`, orgID,
	).Scan(&appID, &apiVersion)
	if err != nil || appID == "" {
		return "", errors.New("facebookads: configura el App ID antes de conectar")
	}
	if apiVersion == "" {
		apiVersion = "v23.0"
	}

	state, err := randomHex(16)
	if err != nil {
		return "", fmt.Errorf("facebookads: generar state: %w", err)
	}

	s.mu.Lock()
	s.oauthStates[state] = oauthStateEntry{orgID: orgID, expiresAt: time.Now().Add(10 * time.Minute)}
	// limpiar estados expirados mientras tenemos el lock
	for k, v := range s.oauthStates {
		if time.Now().After(v.expiresAt) {
			delete(s.oauthStates, k)
		}
	}
	s.mu.Unlock()

	params := url.Values{
		"client_id":     {appID},
		"redirect_uri":  {s.redirectURI},
		"state":         {state},
		"scope":         {"ads_management,pages_manage_ads,ads_read,pages_read_engagement"},
		"response_type": {"code"},
	}
	return fmt.Sprintf("https://www.facebook.com/%s/dialog/oauth?%s", apiVersion, params.Encode()), nil
}

// HandleCallback procesa el callback de Meta: valida el state, intercambia el
// código por un long-lived token (60 días) y lo guarda cifrado en DB.
func (s *Service) HandleCallback(ctx context.Context, code, state string) error {
	s.mu.Lock()
	entry, ok := s.oauthStates[state]
	if ok {
		delete(s.oauthStates, state)
	}
	s.mu.Unlock()

	if !ok || time.Now().After(entry.expiresAt) {
		return errors.New("facebookads: state inválido o expirado (CSRF)")
	}
	orgID := entry.orgID

	var appID, appSecretEnc, apiVersion string
	err := s.pool.QueryRow(ctx,
		`SELECT app_id, app_secret_enc, api_version FROM facebook_ads_configs WHERE organization_id = $1`,
		orgID,
	).Scan(&appID, &appSecretEnc, &apiVersion)
	if err != nil {
		return fmt.Errorf("facebookads: leer config: %w", err)
	}
	if appSecretEnc == "" {
		return errors.New("facebookads: App Secret no configurado")
	}

	appSecretBytes, err := s.enc.Decrypt(appSecretEnc)
	if err != nil {
		return fmt.Errorf("facebookads: descifrar App Secret: %w", err)
	}
	appSecret := string(appSecretBytes)

	// code → short-lived token
	shortToken, err := s.exchangeCode(ctx, appID, appSecret, apiVersion, code)
	if err != nil {
		return err
	}

	// short-lived → long-lived (60 días)
	longToken, err := s.exchangeForLongLived(ctx, appID, appSecret, apiVersion, shortToken)
	if err != nil {
		longToken = shortToken // fallback al token corto
	}

	tokenEnc, err := s.enc.Encrypt([]byte(longToken))
	if err != nil {
		return fmt.Errorf("facebookads: cifrar token: %w", err)
	}

	_, err = s.pool.Exec(ctx, `
		UPDATE facebook_ads_configs
		SET access_token_enc=$1, is_connected=true, connected_at=now(), updated_at=now()
		WHERE organization_id=$2`, tokenEnc, orgID)
	return err
}

// exchangeCode intercambia el authorization code por un short-lived access token.
func (s *Service) exchangeCode(ctx context.Context, appID, appSecret, apiVersion, code string) (string, error) {
	params := url.Values{
		"client_id":     {appID},
		"client_secret": {appSecret},
		"redirect_uri":  {s.redirectURI},
		"code":          {code},
	}
	endpoint := fmt.Sprintf("%s/%s/oauth/access_token?%s", graphBaseURL, apiVersion, params.Encode())
	body, err := httpGet(ctx, endpoint)
	if err != nil {
		return "", fmt.Errorf("facebookads: intercambiar código: %w", err)
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.AccessToken == "" {
		return "", errors.New("facebookads: respuesta de token inválida")
	}
	return resp.AccessToken, nil
}

// exchangeForLongLived intercambia un short-lived token por uno de larga duración.
func (s *Service) exchangeForLongLived(ctx context.Context, appID, appSecret, apiVersion, shortToken string) (string, error) {
	params := url.Values{
		"grant_type":        {"fb_exchange_token"},
		"client_id":         {appID},
		"client_secret":     {appSecret},
		"fb_exchange_token": {shortToken},
	}
	endpoint := fmt.Sprintf("%s/%s/oauth/access_token?%s", graphBaseURL, apiVersion, params.Encode())
	body, err := httpGet(ctx, endpoint)
	if err != nil {
		return "", fmt.Errorf("facebookads: token largo: %w", err)
	}
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.AccessToken == "" {
		return "", errors.New("facebookads: token largo inválido")
	}
	return resp.AccessToken, nil
}

// ── Discovery ─────────────────────────────────────────────────────────────────

// ListAdAccounts devuelve las cuentas publicitarias accesibles con el token guardado.
func (s *Service) ListAdAccounts(ctx context.Context, orgID uuid.UUID) ([]AdAccount, error) {
	accessToken, _, apiVersion, err := s.loadCredentials(ctx, orgID)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/%s/me/adaccounts?fields=id,name,account_status&access_token=%s",
		graphBaseURL, apiVersion, url.QueryEscape(accessToken))
	body, err := httpGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("facebookads: listar cuentas: %w", err)
	}
	var resp struct {
		Data []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status int    `json:"account_status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("facebookads: parsear cuentas: %w", err)
	}
	accounts := make([]AdAccount, len(resp.Data))
	for i, a := range resp.Data {
		accounts[i] = AdAccount{ID: a.ID, Name: a.Name, Status: a.Status}
	}
	return accounts, nil
}

// ListPages devuelve las páginas de Facebook accesibles con el token guardado.
func (s *Service) ListPages(ctx context.Context, orgID uuid.UUID) ([]FbPage, error) {
	accessToken, _, apiVersion, err := s.loadCredentials(ctx, orgID)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/%s/me/accounts?fields=id,name&access_token=%s",
		graphBaseURL, apiVersion, url.QueryEscape(accessToken))
	body, err := httpGet(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("facebookads: listar páginas: %w", err)
	}
	var resp struct {
		Data []struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("facebookads: parsear páginas: %w", err)
	}
	pages := make([]FbPage, len(resp.Data))
	for i, p := range resp.Data {
		pages[i] = FbPage{ID: p.ID, Name: p.Name}
	}
	return pages, nil
}

// SetSelection guarda la cuenta publicitaria y página seleccionadas sin resetear
// is_connected (la conexión OAuth sigue válida).
func (s *Service) SetSelection(ctx context.Context, orgID uuid.UUID, adAccountID, pageID string) (*Config, error) {
	var cfg Config
	cfg.OrganizationID = orgID
	var appSecretEnc, accessTokenEnc string
	err := s.pool.QueryRow(ctx, `
		UPDATE facebook_ads_configs
		SET ad_account_id=$1, page_id=$2, updated_at=now()
		WHERE organization_id=$3
		RETURNING id, app_id, app_secret_enc, access_token_enc,
		          ad_account_id, page_id, business_id, api_version,
		          is_connected, connected_at, updated_at`,
		adAccountID, pageID, orgID,
	).Scan(
		&cfg.ID, &cfg.AppID, &appSecretEnc, &accessTokenEnc,
		&cfg.AdAccountID, &cfg.PageID, &cfg.BusinessID, &cfg.APIVersion,
		&cfg.IsConnected, &cfg.ConnectedAt, &cfg.UpdatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrConfigNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("facebookads: guardar selección: %w", err)
	}
	cfg.AppSecretSet = appSecretEnc != ""
	cfg.AccessTokenSet = accessTokenEnc != ""
	return &cfg, nil
}

// Disconnect borra el access token y resetea el estado de conexión.
func (s *Service) Disconnect(ctx context.Context, orgID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE facebook_ads_configs
		SET access_token_enc='', is_connected=false, connected_at=null,
		    ad_account_id='', page_id='', updated_at=now()
		WHERE organization_id=$1`, orgID)
	return err
}

// ── Helpers internos ──────────────────────────────────────────────────────────

func (s *Service) loadCredentials(ctx context.Context, orgID uuid.UUID) (accessToken, adAccountID, apiVersion string, err error) {
	var tokenEnc, accountID, version string
	row := s.pool.QueryRow(ctx,
		`SELECT access_token_enc, ad_account_id, api_version FROM facebook_ads_configs WHERE organization_id = $1`,
		orgID)
	if scanErr := row.Scan(&tokenEnc, &accountID, &version); errors.Is(scanErr, pgx.ErrNoRows) {
		return "", "", "", ErrConfigNotFound
	} else if scanErr != nil {
		return "", "", "", fmt.Errorf("facebookads: leer credenciales: %w", scanErr)
	}

	if tokenEnc == "" {
		return "", "", "", errors.New("facebookads: access token no configurado")
	}

	tokenBytes, err := s.enc.Decrypt(tokenEnc)
	if err != nil {
		return "", "", "", fmt.Errorf("facebookads: descifrar token: %w", err)
	}
	return string(tokenBytes), accountID, version, nil
}

func (s *Service) setConnected(ctx context.Context, orgID uuid.UUID, connected bool) {
	var connectedAt interface{}
	if connected {
		connectedAt = time.Now()
	}
	_, _ = s.pool.Exec(ctx,
		`UPDATE facebook_ads_configs SET is_connected=$1, connected_at=$2, updated_at=now() WHERE organization_id=$3`,
		connected, connectedAt, orgID)
}

// publishToMeta orquesta campaign → adset → creative → ad en Meta Graph API.
// Actualiza los IDs en DB. Si falla, marca el draft con status "Error".
func (s *Service) publishToMeta(ctx context.Context, orgID uuid.UUID, draft *AdDraft) {
	accessToken, adAccountID, apiVersion, err := s.loadCredentials(ctx, orgID)
	if err != nil {
		s.markAdError(ctx, draft.ID)
		return
	}

	accountID := normalizeAccountID(adAccountID)
	g := &graphClient{accessToken: accessToken, apiVersion: apiVersion, accountID: accountID}

	// 1. Campaña
	campaignID, err := g.createCampaign(ctx, draft.CampaignName, draft.Objective)
	if err != nil {
		s.markAdError(ctx, draft.ID)
		return
	}

	// 2. Ad set
	adSetID, err := g.createAdSet(ctx, draft.CampaignName+" - Set", campaignID, draft.DailyBudgetCents)
	if err != nil {
		s.markAdError(ctx, draft.ID)
		return
	}

	// 3. Creative
	pageID := s.getPageID(ctx, orgID)
	creativeID, err := g.createCreative(ctx, draft.CampaignName+" - Creative", pageID, draft.AdTitle, draft.AdBody, draft.LinkURL)
	if err != nil {
		s.markAdError(ctx, draft.ID)
		return
	}

	// 4. Ad
	adID, err := g.createAd(ctx, draft.CampaignName+" - Ad", adSetID, creativeID)
	if err != nil {
		s.markAdError(ctx, draft.ID)
		return
	}

	_, _ = s.pool.Exec(ctx,
		`UPDATE facebook_ad_drafts SET status='Publicado', campaign_id=$1, ad_set_id=$2, ad_id=$3 WHERE id=$4`,
		campaignID, adSetID, adID, draft.ID)
}

func (s *Service) markAdError(ctx context.Context, adID uuid.UUID) {
	_, _ = s.pool.Exec(ctx, `UPDATE facebook_ad_drafts SET status='Error' WHERE id=$1`, adID)
}

func (s *Service) getPageID(ctx context.Context, orgID uuid.UUID) string {
	var pageID string
	_ = s.pool.QueryRow(ctx,
		`SELECT page_id FROM facebook_ads_configs WHERE organization_id=$1`, orgID).Scan(&pageID)
	return pageID
}

// ── Graph API client ──────────────────────────────────────────────────────────

type graphClient struct {
	accessToken string
	apiVersion  string
	accountID   string
}

type graphError struct {
	Error struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (g *graphClient) post(ctx context.Context, path string, params url.Values) ([]byte, error) {
	params.Set("access_token", g.accessToken)
	endpoint := fmt.Sprintf("%s/%s/%s/%s", graphBaseURL, g.apiVersion, g.accountID, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint,
		strings.NewReader(params.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		var gErr graphError
		_ = json.Unmarshal(body, &gErr)
		msg := gErr.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("graph API %s: %s", path, msg)
	}
	return body, nil
}

func (g *graphClient) createCampaign(ctx context.Context, name, objective string) (string, error) {
	params := url.Values{
		"name":                   {name},
		"objective":              {objective},
		"status":                 {"PAUSED"},
		"special_ad_categories":  {`["EMPLOYMENT"]`},
	}
	body, err := g.post(ctx, "campaigns", params)
	if err != nil {
		return "", err
	}
	var r struct{ ID string `json:"id"` }
	return r.ID, json.Unmarshal(body, &r)
}

func (g *graphClient) createAdSet(ctx context.Context, name, campaignID string, dailyBudgetCents int) (string, error) {
	params := url.Values{
		"name":               {name},
		"campaign_id":        {campaignID},
		"daily_budget":       {fmt.Sprintf("%d", dailyBudgetCents)},
		"billing_event":      {"IMPRESSIONS"},
		"optimization_goal":  {"LINK_CLICKS"},
		"bid_strategy":       {"LOWEST_COST_WITHOUT_CAP"},
		"targeting":          {`{"geo_locations":{"countries":["MX"]}}`},
		"status":             {"PAUSED"},
	}
	body, err := g.post(ctx, "adsets", params)
	if err != nil {
		return "", err
	}
	var r struct{ ID string `json:"id"` }
	return r.ID, json.Unmarshal(body, &r)
}

func (g *graphClient) createCreative(ctx context.Context, name, pageID, title, adBody, linkURL string) (string, error) {
	storySpec, _ := json.Marshal(map[string]any{
		"page_id": pageID,
		"link_data": map[string]string{
			"link":    linkURL,
			"message": adBody,
			"name":    title,
		},
	})
	params := url.Values{
		"name":               {name},
		"object_story_spec":  {string(storySpec)},
	}
	body, err := g.post(ctx, "adcreatives", params)
	if err != nil {
		return "", err
	}
	var r struct{ ID string `json:"id"` }
	return r.ID, json.Unmarshal(body, &r)
}

func (g *graphClient) createAd(ctx context.Context, name, adSetID, creativeID string) (string, error) {
	creative, _ := json.Marshal(map[string]string{"creative_id": creativeID})
	params := url.Values{
		"name":      {name},
		"adset_id":  {adSetID},
		"creative":  {string(creative)},
		"status":    {"PAUSED"},
	}
	body, err := g.post(ctx, "ads", params)
	if err != nil {
		return "", err
	}
	var r struct{ ID string `json:"id"` }
	return r.ID, json.Unmarshal(body, &r)
}

// ── Utilidades ────────────────────────────────────────────────────────────────

func randomHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func normalizeAccountID(id string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "act_") {
		return id
	}
	return "act_" + id
}

func httpGet(ctx context.Context, endpoint string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		var gErr graphError
		_ = json.Unmarshal(body, &gErr)
		msg := gErr.Error.Message
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return nil, fmt.Errorf("graph API: %s", msg)
	}
	return body, nil
}

func validateCreateAdInput(in CreateAdInput) error {
	if strings.TrimSpace(in.CampaignName) == "" {
		return errors.New("campaignName es requerido")
	}
	if !validObjectives[in.Objective] {
		return fmt.Errorf("objective inválido: %q", in.Objective)
	}
	if in.DailyBudgetCents < 100 {
		return errors.New("dailyBudgetCents debe ser al menos 100 (MXN $1.00)")
	}
	if strings.TrimSpace(in.AdTitle) == "" {
		return errors.New("adTitle es requerido")
	}
	if strings.TrimSpace(in.AdBody) == "" {
		return errors.New("adBody es requerido")
	}
	if strings.TrimSpace(in.LinkURL) == "" {
		return errors.New("linkUrl es requerido")
	}
	return nil
}
