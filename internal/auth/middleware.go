package auth

import (
	"context"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/FAMMTO/reclutamiento_backend/internal/platform/web"
)

type contextKey string

const claimsKey contextKey = "auth.claims"

// Identity es la identidad autenticada disponible para los handlers.
type Identity struct {
	RecruiterID     uuid.UUID
	OrganizationID  uuid.UUID
	Role            string
	PasswordChanged bool
}

func IdentityFrom(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(claimsKey).(Identity)
	return identity, ok
}

// RequireAuth valida el Bearer token y coloca la identidad en el contexto.
func RequireAuth(jwtSecret []byte) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			header := r.Header.Get("Authorization")
			token, ok := strings.CutPrefix(header, "Bearer ")
			if !ok || token == "" {
				web.RespondError(w, http.StatusUnauthorized, "unauthorized", "Se requiere autenticación")
				return
			}
			claims, err := ParseAccessToken(jwtSecret, token)
			if err != nil {
				web.RespondError(w, http.StatusUnauthorized, "unauthorized", "Sesión inválida o expirada")
				return
			}
			recruiterID, err := uuid.Parse(claims.Subject)
			if err != nil {
				web.RespondError(w, http.StatusUnauthorized, "unauthorized", "Sesión inválida")
				return
			}
			orgID, err := uuid.Parse(claims.OrganizationID)
			if err != nil {
				web.RespondError(w, http.StatusUnauthorized, "unauthorized", "Sesión inválida")
				return
			}
			identity := Identity{
				RecruiterID:     recruiterID,
				OrganizationID:  orgID,
				Role:            claims.Role,
				PasswordChanged: claims.PasswordChanged,
			}
			next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), claimsKey, identity)))
		})
	}
}

// RequirePasswordChanged bloquea el acceso al sistema mientras el usuario no
// haya reemplazado su contraseña temporal (regla del flujo de alta).
func RequirePasswordChanged(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			web.RespondError(w, http.StatusUnauthorized, "unauthorized", "Se requiere autenticación")
			return
		}
		if !identity.PasswordChanged {
			web.RespondError(w, http.StatusForbidden, "password_change_required",
				"Debes cambiar tu contraseña temporal antes de continuar")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdmin permite el paso solo a reclutadores con rol Administrador.
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		identity, ok := IdentityFrom(r.Context())
		if !ok {
			web.RespondError(w, http.StatusUnauthorized, "unauthorized", "Se requiere autenticación")
			return
		}
		if identity.Role != "Administrador" {
			web.RespondError(w, http.StatusForbidden, "forbidden",
				"Esta acción requiere permiso de Administrador")
			return
		}
		next.ServeHTTP(w, r)
	})
}
