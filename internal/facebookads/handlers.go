package facebookads

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/FAMMTO/reclutamiento_backend/internal/auth"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/web"
)

type Handlers struct {
	svc *Service
}

func NewHandlers(svc *Service) *Handlers {
	return &Handlers{svc: svc}
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
	err = h.svc.DeleteAd(r.Context(), identity.OrganizationID, adID)
	if errors.Is(err, ErrAdNotFound) {
		web.RespondError(w, http.StatusNotFound, "not_found", "anuncio no encontrado")
		return
	}
	if err != nil {
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
