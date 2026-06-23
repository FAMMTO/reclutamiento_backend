-- 0005: tokens de un solo uso para restablecer contraseña (expiran en 30 min)
CREATE TABLE password_reset_tokens (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    recruiter_id uuid        NOT NULL REFERENCES recruiters(id) ON DELETE CASCADE,
    token_hash   text        NOT NULL UNIQUE,
    expires_at   timestamptz NOT NULL,
    used_at      timestamptz,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX ON password_reset_tokens(recruiter_id);
