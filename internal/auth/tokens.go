package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

const jwtIssuer = "jobbly-api"

type AccessClaims struct {
	Role            string `json:"role"`
	OrganizationID  string `json:"org"`
	PasswordChanged bool   `json:"pwc"`
	jwt.RegisteredClaims
}

func NewAccessToken(secret []byte, ttl time.Duration, recruiterID, orgID uuid.UUID, role string, passwordChanged bool) (string, error) {
	now := time.Now()
	claims := AccessClaims{
		Role:            role,
		OrganizationID:  orgID.String(),
		PasswordChanged: passwordChanged,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    jwtIssuer,
			Subject:   recruiterID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func ParseAccessToken(secret []byte, tokenString string) (*AccessClaims, error) {
	claims := &AccessClaims{}
	token, err := jwt.ParseWithClaims(tokenString, claims,
		func(t *jwt.Token) (any, error) { return secret, nil },
		jwt.WithValidMethods([]string{"HS256"}),
		jwt.WithIssuer(jwtIssuer),
		jwt.WithExpirationRequired(),
	)
	if err != nil || !token.Valid {
		return nil, errors.New("token inválido o expirado")
	}
	return claims, nil
}

// NewRefreshToken genera un token opaco (32 bytes aleatorios). En la base de
// datos solo se guarda su hash SHA-256: si la DB se filtra, los tokens no sirven.
func NewRefreshToken() (token string, tokenHash string, err error) {
	raw := make([]byte, 32)
	if _, err = rand.Read(raw); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw)
	return token, HashRefreshToken(token), nil
}

func HashRefreshToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
