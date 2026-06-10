package auth

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

var testSecret = []byte("secreto-de-pruebas-con-al-menos-32-chars!")

func TestAccessTokenRoundTrip(t *testing.T) {
	recruiterID := uuid.New()
	orgID := uuid.New()

	token, err := NewAccessToken(testSecret, time.Minute, recruiterID, orgID, "Administrador", true)
	if err != nil {
		t.Fatalf("NewAccessToken: %v", err)
	}

	claims, err := ParseAccessToken(testSecret, token)
	if err != nil {
		t.Fatalf("ParseAccessToken: %v", err)
	}
	if claims.Subject != recruiterID.String() {
		t.Errorf("subject = %s, esperaba %s", claims.Subject, recruiterID)
	}
	if claims.Role != "Administrador" || claims.OrganizationID != orgID.String() || !claims.PasswordChanged {
		t.Errorf("claims incorrectos: %+v", claims)
	}
}

func TestAccessTokenRejectsWrongSecret(t *testing.T) {
	token, _ := NewAccessToken(testSecret, time.Minute, uuid.New(), uuid.New(), "Ejecutivo", true)
	otherSecret := []byte("otro-secreto-distinto-tambien-de-32-chars")
	if _, err := ParseAccessToken(otherSecret, token); err == nil {
		t.Fatal("un token firmado con otro secreto debe rechazarse")
	}
}

func TestAccessTokenRejectsExpired(t *testing.T) {
	token, _ := NewAccessToken(testSecret, -time.Minute, uuid.New(), uuid.New(), "Ejecutivo", true)
	if _, err := ParseAccessToken(testSecret, token); err == nil {
		t.Fatal("un token expirado debe rechazarse")
	}
}

func TestRefreshTokenHashing(t *testing.T) {
	token, hash, err := NewRefreshToken()
	if err != nil {
		t.Fatalf("NewRefreshToken: %v", err)
	}
	if token == "" || hash == "" {
		t.Fatal("token o hash vacíos")
	}
	if HashRefreshToken(token) != hash {
		t.Fatal("el hash no es determinista")
	}
	if token == hash {
		t.Fatal("el token no debe guardarse en claro")
	}

	otherToken, otherHash, _ := NewRefreshToken()
	if otherToken == token || otherHash == hash {
		t.Fatal("dos refresh tokens consecutivos no deben repetirse")
	}
}
