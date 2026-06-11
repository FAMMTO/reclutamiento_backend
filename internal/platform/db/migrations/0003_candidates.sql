-- 0003_candidates: candidatos y postulaciones (Fase 3).
-- Los candidatos llegan por el flujo público InfoMatch; el teléfono es su
-- identificador natural dentro de cada organización.

CREATE TABLE candidates (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    phone           text NOT NULL,
    name            text NOT NULL,
    age             int,
    email           text NOT NULL DEFAULT '',
    state           text NOT NULL DEFAULT '',
    municipality    text NOT NULL DEFAULT '',
    education       text NOT NULL DEFAULT '',
    degree          text NOT NULL DEFAULT '',
    certifications  text[] NOT NULL DEFAULT '{}',
    desired_salary  text NOT NULL DEFAULT '',
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, phone)
);

CREATE TABLE applications (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    candidate_id    uuid NOT NULL REFERENCES candidates(id) ON DELETE CASCADE,
    vacancy_id      uuid NOT NULL REFERENCES vacancies(id) ON DELETE CASCADE,
    answers         jsonb NOT NULL DEFAULT '[]'::jsonb,
    status          text NOT NULL DEFAULT 'nueva'
                    CHECK (status IN ('nueva', 'en_revision', 'entrevista', 'rechazada', 'contratada')),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (candidate_id, vacancy_id)
);

CREATE INDEX idx_applications_vacancy ON applications (vacancy_id, status);
CREATE INDEX idx_applications_org ON applications (organization_id, created_at DESC);
CREATE INDEX idx_candidates_org ON candidates (organization_id, created_at DESC);
