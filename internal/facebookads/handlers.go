package facebookads

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/FAMMTO/reclutamiento_backend/internal/auth"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/web"
)

type Handlers struct {
	svc         *Service
	frontendURL string
}

func NewHandlers(svc *Service, frontendURL string) *Handlers {
	return &Handlers{svc: svc, frontendURL: frontendURL}
}

// GET /facebookads/config
func (h *Handlers) GetConfig(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	cfg, err := h.svc.GetConfig(r.Context(), identity.OrganizationID)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, cfg)
}

// PUT /facebookads/config
func (h *Handlers) SaveConfig(w http.ResponseWriter, r *http.Request) {
	var in SaveConfigInput
	if err := web.DecodeJSON(w, r, &in); err != nil {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	cfg, err := h.svc.SaveConfig(r.Context(), identity.OrganizationID, in)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, cfg)
}

// POST /facebookads/test
func (h *Handlers) TestConnection(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	accountName, err := h.svc.TestConnection(r.Context(), identity.OrganizationID)
	if errors.Is(err, ErrConfigNotFound) {
		web.RespondError(w, http.StatusBadRequest, "config_missing", "no hay configuración guardada")
		return
	}
	if err != nil {
		web.RespondError(w, http.StatusBadGateway, "graph_api_error", err.Error())
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]any{"accountName": accountName})
}

// GET /facebookads/ads
func (h *Handlers) ListAds(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	ads, err := h.svc.ListAds(r.Context(), identity.OrganizationID)
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]any{"ads": ads})
}

// POST /facebookads/ads
func (h *Handlers) CreateAd(w http.ResponseWriter, r *http.Request) {
	var in CreateAdInput
	if err := web.DecodeJSON(w, r, &in); err != nil {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	draft, err := h.svc.CreateAd(r.Context(), identity.OrganizationID, in)
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	web.RespondJSON(w, http.StatusCreated, draft)
}

// PATCH /facebookads/ads/{id}
func (h *Handlers) SetAdStatus(w http.ResponseWriter, r *http.Request) {
	adID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	draft, err := h.svc.SetAdStatus(r.Context(), identity.OrganizationID, adID, body.Status)
	if errors.Is(err, ErrAdNotFound) {
		web.RespondError(w, http.StatusNotFound, "not_found", "anuncio no encontrado")
		return
	}
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "validation", err.Error())
		return
	}
	web.RespondJSON(w, http.StatusOK, draft)
}

// DELETE /facebookads/ads/{id}
func (h *Handlers) DeleteAd(w http.ResponseWriter, r *http.Request) {
	adID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "id inválido")
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.svc.DeleteAd(r.Context(), identity.OrganizationID, adID); errors.Is(err, ErrAdNotFound) {
		web.RespondError(w, http.StatusNotFound, "not_found", "anuncio no encontrado")
		return
	} else if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ── OAuth ─────────────────────────────────────────────────────────────────────

// GET /facebookads/oauth/url  — autenticado, devuelve la URL de autorización
func (h *Handlers) OAuthURL(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	oauthURL, err := h.svc.GenerateOAuthURL(r.Context(), identity.OrganizationID)
	if err != nil {
		web.RespondError(w, http.StatusBadRequest, "config_missing", err.Error())
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]string{"url": oauthURL})
}

// GET /facebookads/oauth/callback  — público, el browser llega aquí desde Meta
func (h *Handlers) OAuthCallback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")

	// Meta rechazó o el usuario canceló
	if code == "" {
		msg := r.URL.Query().Get("error_description")
		if msg == "" {
			msg = r.URL.Query().Get("error")
		}
		http.Redirect(w, r,
			h.frontendURL+"/dashboard?fb=error&message="+url.QueryEscape(msg),
			http.StatusFound)
		return
	}

	if err := h.svc.HandleCallback(r.Context(), code, state); err != nil {
		http.Redirect(w, r,
			h.frontendURL+"/dashboard?fb=error&message="+url.QueryEscape(err.Error()),
			http.StatusFound)
		return
	}
	http.Redirect(w, r, h.frontendURL+"/dashboard?fb=connected", http.StatusFound)
}

// GET /facebookads/accounts  — autenticado, lista cuentas publicitarias
func (h *Handlers) ListAccounts(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	accounts, err := h.svc.ListAdAccounts(r.Context(), identity.OrganizationID)
	if errors.Is(err, ErrConfigNotFound) {
		web.RespondError(w, http.StatusBadRequest, "not_connected", "conecta Facebook primero")
		return
	}
	if err != nil {
		web.RespondError(w, http.StatusBadGateway, "graph_api_error", err.Error())
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

// GET /facebookads/pages  — autenticado, lista páginas de Facebook
func (h *Handlers) ListPages(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	pages, err := h.svc.ListPages(r.Context(), identity.OrganizationID)
	if errors.Is(err, ErrConfigNotFound) {
		web.RespondError(w, http.StatusBadRequest, "not_connected", "conecta Facebook primero")
		return
	}
	if err != nil {
		web.RespondError(w, http.StatusBadGateway, "graph_api_error", err.Error())
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]any{"pages": pages})
}

// PUT /facebookads/selection  — guarda cuenta + página elegidas sin resetear la conexión
func (h *Handlers) SetSelection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AdAccountID string `json:"adAccountId"`
		PageID      string `json:"pageId"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		return
	}
	identity, _ := auth.IdentityFrom(r.Context())
	cfg, err := h.svc.SetSelection(r.Context(), identity.OrganizationID, body.AdAccountID, body.PageID)
	if errors.Is(err, ErrConfigNotFound) {
		web.RespondError(w, http.StatusNotFound, "not_found", "configuración no encontrada")
		return
	}
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, cfg)
}

// POST /facebookads/disconnect  — borra el token y resetea el estado
func (h *Handlers) Disconnect(w http.ResponseWriter, r *http.Request) {
	identity, _ := auth.IdentityFrom(r.Context())
	if err := h.svc.Disconnect(r.Context(), identity.OrganizationID); err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
