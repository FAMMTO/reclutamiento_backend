// Package config carga y valida la configuración del servicio desde variables
// de entorno. Falla rápido en el arranque si falta algo crítico.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	// HTTP
	Port        string
	CORSOrigins []string

	// Base de datos (local en dev, VPS en producción vía DATABASE_URL)
	DatabaseURL string

	// Auth
	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	CookieSecure    bool
	CookieDomain    string

	// Cifrado en reposo (AES-256-GCM para secretos de terceros en Postgres)
	EncryptionKey []byte

	// Seed del primer administrador (solo se aplica si no existe ninguno)
	SeedAdminName     string
	SeedAdminEmail    string
	SeedAdminPassword string

	Env string // "development" | "production"
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:              getenv("PORT", "4000"),
		DatabaseURL:       os.Getenv("DATABASE_URL"),
		AccessTokenTTL:    getDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL:   getDuration("REFRESH_TOKEN_TTL", 7*24*time.Hour),
		CookieSecure:      getBool("COOKIE_SECURE", false),
		CookieDomain:      os.Getenv("COOKIE_DOMAIN"),
		SeedAdminName:     getenv("SEED_ADMIN_NAME", "Administrador Inicial"),
		SeedAdminEmail:    os.Getenv("SEED_ADMIN_EMAIL"),
		SeedAdminPassword: os.Getenv("SEED_ADMIN_PASSWORD"),
		Env:               getenv("APP_ENV", "development"),
	}

	for _, origin := range strings.Split(getenv("CORS_ORIGINS", "http://localhost:8080,http://localhost:5173"), ",") {
		origin = strings.TrimSpace(origin)
		if origin != "" {
			cfg.CORSOrigins = append(cfg.CORSOrigins, origin)
		}
	}

	if cfg.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL es obligatoria (ej. postgres://user:pass@host:5432/jobbly?sslmode=require)")
	}

	secret := os.Getenv("JWT_SECRET")
	if len(secret) < 32 {
		return nil, fmt.Errorf("JWT_SECRET debe tener al menos 32 caracteres (tiene %d)", len(secret))
	}
	cfg.JWTSecret = []byte(secret)

	encKey := os.Getenv("ENCRYPTION_KEY")
	if len(encKey) != 32 {
		return nil, fmt.Errorf("ENCRYPTION_KEY debe tener exactamente 32 caracteres (tiene %d)", len(encKey))
	}
	cfg.EncryptionKey = []byte(encKey)

	if cfg.Env == "production" {
		if !cfg.CookieSecure {
			return nil, fmt.Errorf("COOKIE_SECURE debe ser true en producción")
		}
		if !strings.Contains(cfg.DatabaseURL, "sslmode=require") &&
			!strings.Contains(cfg.DatabaseURL, "sslmode=verify") {
			return nil, fmt.Errorf("en producción DATABASE_URL debe usar sslmode=require o superior (Postgres remoto en VPS)")
		}
	}

	return cfg, nil
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func getBool(key string, fallback bool) bool {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func getDuration(key string, fallback time.Duration) time.Duration {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
