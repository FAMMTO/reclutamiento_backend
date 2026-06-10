// Package web concentra helpers HTTP compartidos: respuestas JSON con sobre
// de error uniforme y decodificación segura de cuerpos de petición.
package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
)

const maxBodyBytes = 1 << 20 // 1 MiB

type ErrorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type errorEnvelope struct {
	Error ErrorBody `json:"error"`
}

func RespondJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if payload != nil {
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			slog.Error("error serializando respuesta", "err", err)
		}
	}
}

func RespondError(w http.ResponseWriter, status int, code, message string) {
	RespondJSON(w, status, errorEnvelope{Error: ErrorBody{Code: code, Message: message}})
}

// DecodeJSON limita el tamaño del cuerpo y rechaza campos desconocidos.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			return fmt.Errorf("cuerpo de petición demasiado grande")
		}
		return fmt.Errorf("JSON inválido: %w", err)
	}
	if decoder.More() {
		return errors.New("el cuerpo debe contener un solo objeto JSON")
	}
	return nil
}
