-- 0001_init: organizaciones, reclutadores, refresh tokens y bitácora de auditoría.
-- Esquema según DATABASE_VARIABLES.md y PLAN_DE_ACCION.md §2.2.

CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE organizations (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE recruiters (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  uuid NOT NULL REFERENCES organizations(id),
    name             text NOT NULL,
    email            citext NOT NULL UNIQUE,
    phone            text NOT NULL DEFAULT '',
    password_hash    text NOT NULL,
    permission       text NOT NULL CHECK (permission IN ('Administrador', 'Ejecutivo')),
    created_by       uuid REFERENCES recruiters(id),
    is_active        boolean NOT NULL DEFAULT true,
    password_changed boolean NOT NULL DEFAULT false,
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_recruiters_organization ON recruiters (organization_id);

CREATE TABLE refresh_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    recruiter_id uuid NOT NULL REFERENCES recruiters(id) ON DELETE CASCADE,
    token_hash   text NOT NULL UNIQUE,
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_refresh_tokens_recruiter ON refresh_tokens (recruiter_id);

CREATE TABLE audit_log (
    id        bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    actor_id  uuid,
    action    text NOT NULL,
    entity    text NOT NULL DEFAULT '',
    entity_id text NOT NULL DEFAULT '',
    detail    jsonb NOT NULL DEFAULT '{}'::jsonb,
    ip        text NOT NULL DEFAULT '',
    at        timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_audit_log_actor ON audit_log (actor_id, at DESC);
