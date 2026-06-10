# Jobbly Backend

API de reclutamiento en **Go 1.26 + PostgreSQL 16**. Frontend: [`RECLUTAMIENTO-AI`](../RECLUTAMIENTO-AI).
Contrato: [`api/openapi.yaml`](api/openapi.yaml) · Plan: [`../PLAN_DE_ACCION.md`](../PLAN_DE_ACCION.md) · Reglas de trabajo: [`CLAUDE.md`](CLAUDE.md).

## Arranque rápido (desarrollo)

Requisitos: Go 1.26+, PostgreSQL 16 (local o Docker).

```bash
# 1. Base de datos local (brew) …
brew services start postgresql@16
createdb jobbly_dev

#    … o con Docker
docker compose up postgres -d

# 2. Configuración
cp .env.example .env          # ajustar JWT_SECRET y SEED_ADMIN_*

# 3. Correr el API (aplica migraciones y crea el admin inicial si no hay reclutadores)
set -a; source .env; set +a
go run ./cmd/api
```

El servidor escucha en `:4000`. Salud: `GET /healthz`, `GET /readyz`.

## Tests

```bash
go test ./...                                                          # unitarios
createdb jobbly_test
TEST_DATABASE_URL="postgres://localhost:5432/jobbly_test?sslmode=disable" go test ./...   # + integración
```

Los tests de integración usan `TEST_DATABASE_URL` y **truncan** esa base; nunca apuntarla a datos reales.

## Producción (VPS)

- `DATABASE_URL` apunta al Postgres del VPS con **`sslmode=require`** (el arranque lo exige con `APP_ENV=production`).
- `COOKIE_SECURE=true` (obligatorio en producción) y `CORS_ORIGINS` con el dominio real del frontend.
- `JWT_SECRET` de 48+ caracteres aleatorios: `openssl rand -base64 48`.
- Postgres del VPS: no exponerlo a internet si API y DB conviven en la misma máquina (bind a localhost/red privada + firewall); backups automáticos con prueba de restore.
- El binario es estático (ver `Dockerfile`); detrás de un reverse proxy (Caddy/Nginx) con TLS.

## Seguridad implementada

| Capa | Detalle |
|------|---------|
| Contraseñas | argon2id (OWASP: m=64MiB, t=1, p=4); política ≥10 chars con letras y números |
| Sesiones | JWT access 15 min + refresh opaco rotativo en cookie httpOnly; en DB solo se guarda el hash SHA-256 |
| Robo de refresh | Reusar un token rotado revoca todas las sesiones del usuario (auditado) |
| Primer login | Contraseña temporal bloquea todo el API (403 `password_change_required`) hasta cambiarla |
| RBAC | Roles `Administrador` / `Ejecutivo`; gestión de reclutadores solo para Administrador |
| Rate limiting | 10/min por IP en login; 60/min en refresh; 300/min global |
| CORS | Lista blanca exacta de orígenes con credenciales |
| Auditoría | `audit_log`: logins (éxito/fallo), cambios de contraseña, altas/bajas, detección de reuso |
| Anti-enumeración | Login con email inexistente tarda lo mismo que contraseña incorrecta |

## Estructura

```
cmd/api/                  entrypoint HTTP (solo wiring)
internal/auth/            login, tokens, cambio de contraseña, middleware RBAC
internal/recruiters/      alta/gestión de reclutadores
internal/platform/
  config/                 carga y validación de env
  db/                     pool pgx + migraciones embebidas (db/migrations/*.sql)
  httpserver/             CORS, rate limit, headers, logging, recover
  web/                    respuestas JSON y decode seguro
  audit/                  bitácora de acciones sensibles
api/openapi.yaml          contrato del API
```

Antes de modificar código, leer el **grafo del proyecto** en [`CLAUDE.md`](CLAUDE.md) y actualizarlo con cada cambio.
