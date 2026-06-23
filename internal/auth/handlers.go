package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/FAMMTO/reclutamiento_backend/internal/platform/httpserver"
	"github.com/FAMMTO/reclutamiento_backend/internal/platform/web"
)

const refreshCookieName = "jobbly_refresh"

// EmailSender es la interfaz que usa auth para enviar correos. La implementación
// real vive en internal/platform/email; puede ser nil en dev sin RESEND_API_KEY.
type EmailSender interface {
	SendPasswordReset(ctx context.Context, to, name, resetURL string) error
}

type Handlers struct {
	service      *Service
	cookieSecure bool
	cookieDomain string
	refreshTTL   time.Duration
	emailSender  EmailSender // puede ser nil; en ese caso se omite el envío
	frontendURL  string
}

func NewHandlers(service *Service, cookieSecure bool, cookieDomain string, refreshTTL time.Duration, emailSender EmailSender, frontendURL string) *Handlers {
	return &Handlers{
		service:      service,
		cookieSecure: cookieSecure,
		cookieDomain: cookieDomain,
		refreshTTL:   refreshTTL,
		emailSender:  emailSender,
		frontendURL:  frontendURL,
	}
}

type sessionResponse struct {
	AccessToken string    `json:"accessToken"`
	Recruiter   Recruiter `json:"recruiter"`
}

func (h *Handlers) setRefreshCookie(w http.ResponseWriter, token string, maxAge time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name:     refreshCookieName,
		Value:    token,
		Path:     "/api/v1/auth",
		Domain:   h.cookieDomain,
		MaxAge:   int(maxAge.Seconds()),
		HttpOnly: true,
		Secure:   h.cookieSecure,
		SameSite: http.SameSiteLaxMode,
	})
}

func (h *Handlers) respondSession(w http.ResponseWriter, session *Session) {
	h.setRefreshCookie(w, session.RefreshToken, h.refreshTTL)
	web.RespondJSON(w, http.StatusOK, sessionResponse{
		AccessToken: session.AccessToken,
		Recruiter:   session.Recruiter,
	})
}

// Login maneja POST /api/v1/auth/login
func (h *Handlers) Login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" || body.Password == "" {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "Email y contraseña son obligatorios")
		return
	}

	session, err := h.service.Login(r.Context(), body.Email, body.Password, httpserver.ClientIP(r))
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			web.RespondError(w, http.StatusUnauthorized, "invalid_credentials", "Email o contraseña incorrectos")
			return
		}
		web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		return
	}
	h.respondSession(w, session)
}

// Refresh maneja POST /api/v1/auth/refresh (usa la cookie httpOnly)
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(refreshCookieName)
	if err != nil || cookie.Value == "" {
		web.RespondError(w, http.StatusUnauthorized, "unauthorized", "No hay sesión activa")
		return
	}
	session, err := h.service.Refresh(r.Context(), cookie.Value, httpserver.ClientIP(r))
	if err != nil {
		h.setRefreshCookie(w, "", -1)
		web.RespondError(w, http.StatusUnauthorized, "unauthorized", "Sesión inválida o expirada")
		return
	}
	h.respondSession(w, session)
}

// Logout maneja POST /api/v1/auth/logout
func (h *Handlers) Logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(refreshCookieName); err == nil {
		h.service.Logout(r.Context(), cookie.Value)
	}
	h.setRefreshCookie(w, "", -1)
	web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// Me maneja GET /api/v1/auth/me (requiere RequireAuth)
func (h *Handlers) Me(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	recruiter, err := h.service.GetRecruiter(r.Context(), identity.RecruiterID)
	if err != nil {
		web.RespondError(w, http.StatusUnauthorized, "unauthorized", "Sesión inválida")
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]Recruiter{"recruiter": recruiter})
}

// ForgotPassword maneja POST /api/v1/auth/forgot-password (público, rate limit estricto)
// Siempre responde 200 para no revelar si el email existe.
func (h *Handlers) ForgotPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email string `json:"email"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	body.Email = strings.TrimSpace(body.Email)
	if body.Email == "" {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "El email es obligatorio")
		return
	}

	token, email, name, err := h.service.ForgotPassword(r.Context(), body.Email)
	if err == nil && token != "" && h.emailSender != nil {
		resetURL := h.frontendURL + "/reset-password?token=" + token
		_ = h.emailSender.SendPasswordReset(r.Context(), email, name, resetURL)
	}
	web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ResetPassword maneja POST /api/v1/auth/reset-password (público, rate limit estricto)
func (h *Handlers) ResetPassword(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token       string `json:"token"`
		NewPassword string `json:"newPassword"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if body.Token == "" || body.NewPassword == "" {
		web.RespondError(w, http.StatusBadRequest, "bad_request", "Token y nueva contraseña son obligatorios")
		return
	}

	if err := h.service.ResetPassword(r.Context(), body.Token, body.NewPassword, httpserver.ClientIP(r)); err != nil {
		switch {
		case errors.Is(err, ErrTokenInvalid):
			web.RespondError(w, http.StatusBadRequest, "token_invalid", "El enlace es inválido o ha expirado")
		case errors.Is(err, ErrWeakPassword):
			detail := strings.TrimPrefix(err.Error(), ErrWeakPassword.Error()+"\n")
			web.RespondError(w, http.StatusBadRequest, "weak_password", detail)
		default:
			web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		}
		return
	}
	web.RespondJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ChangePassword maneja POST /api/v1/auth/change-password (requiere RequireAuth)
func (h *Handlers) ChangePassword(w http.ResponseWriter, r *http.Request) {
	identity, _ := IdentityFrom(r.Context())
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if err := web.DecodeJSON(w, r, &body); err != nil {
		web.RespondError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	session, err := h.service.ChangePassword(r.Context(),
		identity.RecruiterID, body.CurrentPassword, body.NewPassword, httpserver.ClientIP(r))
	if err != nil {
		switch {
		case errors.Is(err, ErrInvalidCredentials):
			web.RespondError(w, http.StatusUnauthorized, "invalid_credentials", "La contraseña actual es incorrecta")
		case errors.Is(err, ErrSamePassword):
			web.RespondError(w, http.StatusBadRequest, "same_password", err.Error())
		case errors.Is(err, ErrWeakPassword):
			detail := strings.TrimPrefix(err.Error(), ErrWeakPassword.Error()+"\n")
			web.RespondError(w, http.StatusBadRequest, "weak_password", detail)
		default:
			web.RespondError(w, http.StatusInternalServerError, "internal", "Error interno")
		}
		return
	}
	h.respondSession(w, session)
}
