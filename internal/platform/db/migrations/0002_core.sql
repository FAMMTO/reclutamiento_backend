-- 0002_core: compañías, vacantes y rutas de transporte (Fase 2).
-- Los catálogos de estados/municipios MX permanecen como asset estático del
-- frontend (datos verdaderamente inmutables); la DB guarda los valores en texto.

CREATE TABLE companies (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    name            text NOT NULL,
    created_by      uuid REFERENCES recruiters(id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id, name)
);

CREATE TABLE vacancies (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  uuid NOT NULL REFERENCES organizations(id),
    company_id       uuid NOT NULL REFERENCES companies(id),
    survey_name      text NOT NULL,
    job_title        text NOT NULL,
    job_description  text NOT NULL DEFAULT '',
    state            text NOT NULL DEFAULT '',
    municipality     text NOT NULL DEFAULT '',
    work_mode        text NOT NULL DEFAULT 'Hibrida'
                     CHECK (work_mode IN ('Hibrida', 'Presencial', 'Remoto')),
    salary_range     text NOT NULL DEFAULT '',
    schedule         text NOT NULL DEFAULT '',
    requested_sex    text NOT NULL DEFAULT 'Ambos'
                     CHECK (requested_sex IN ('Hombre', 'Mujer', 'Ambos')),
    education_levels text[] NOT NULL DEFAULT '{}',
    activities       jsonb NOT NULL DEFAULT '[]'::jsonb,
    custom_boxes     jsonb NOT NULL DEFAULT '[]'::jsonb,
    status           text NOT NULL DEFAULT 'draft'
                     CHECK (status IN ('draft', 'published', 'closed')),
    published_at     timestamptz,
    created_by       uuid REFERENCES recruiters(id),
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_vacancies_org_status ON vacancies (organization_id, status, published_at DESC);
-- listado público de vacantes activas
CREATE INDEX idx_vacancies_published ON vacancies (status, published_at DESC)
    WHERE status = 'published';
CREATE INDEX idx_vacancies_company ON vacancies (company_id);

CREATE TABLE rutas (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id uuid NOT NULL REFERENCES organizations(id),
    ubicacion       text NOT NULL,
    horario         text NOT NULL DEFAULT '',
    lat             double precision,
    lng             double precision,
    created_by      uuid REFERENCES recruiters(id),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_rutas_org ON rutas (organization_id);

CREATE TABLE ruta_vacancies (
    ruta_id    uuid NOT NULL REFERENCES rutas(id) ON DELETE CASCADE,
    vacancy_id uuid NOT NULL REFERENCES vacancies(id) ON DELETE CASCADE,
    PRIMARY KEY (ruta_id, vacancy_id)
);
