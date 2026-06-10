// Package audit registra acciones sensibles en la tabla audit_log.
// Las escrituras son best-effort: un fallo de auditoría se loggea pero no
// interrumpe la operación del usuario.
package audit

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Logger struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func New(pool *pgxpool.Pool, log *slog.Logger) *Logger {
	return &Logger{pool: pool, log: log}
}

type Entry struct {
	ActorID  *uuid.UUID
	Action   string // ej. "auth.login", "auth.login_failed", "recruiters.create"
	Entity   string
	EntityID string
	Detail   map[string]any
	IP       string
}

func (l *Logger) Record(ctx context.Context, entry Entry) {
	detail, err := json.Marshal(entry.Detail)
	if err != nil || entry.Detail == nil {
		detail = []byte("{}")
	}
	_, err = l.pool.Exec(ctx,
		`INSERT INTO audit_log (actor_id, action, entity, entity_id, detail, ip)
		 VALUES ($1, $2, $3, $4, $5, $6)`,
		entry.ActorID, entry.Action, entry.Entity, entry.EntityID, detail, entry.IP)
	if err != nil {
		l.log.Error("no se pudo escribir audit_log", "err", err, "action", entry.Action)
	}
}
