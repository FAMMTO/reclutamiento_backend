-- Fase 4: configuración de Facebook Ads (cifrada en reposo) y borradores de anuncios.
-- appSecret y accessToken se almacenan como AES-256-GCM ciphertext en base64.

CREATE TABLE facebook_ads_configs (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  uuid        NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    app_id           text        NOT NULL DEFAULT '',
    app_secret_enc   text        NOT NULL DEFAULT '',  -- AES-GCM base64; vacío = no configurado
    access_token_enc text        NOT NULL DEFAULT '',  -- AES-GCM base64; vacío = no configurado
    ad_account_id    text        NOT NULL DEFAULT '',
    page_id          text        NOT NULL DEFAULT '',
    business_id      text        NOT NULL DEFAULT '',
    api_version      text        NOT NULL DEFAULT 'v23.0',
    is_connected     boolean     NOT NULL DEFAULT false,
    connected_at     timestamptz,
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (organization_id)
);

CREATE TYPE facebook_ad_status AS ENUM ('Borrador', 'Publicado', 'Pausado', 'Error');

CREATE TABLE facebook_ad_drafts (
    id               uuid                PRIMARY KEY DEFAULT gen_random_uuid(),
    organization_id  uuid                NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
    vacancy_id       uuid                REFERENCES vacancies(id) ON DELETE SET NULL,
    campaign_name    text                NOT NULL,
    objective        text                NOT NULL,
    daily_budget_cents int               NOT NULL DEFAULT 0,
    ad_title         text                NOT NULL,
    ad_body          text                NOT NULL,
    link_url         text                NOT NULL,
    status           facebook_ad_status  NOT NULL DEFAULT 'Borrador',
    campaign_id      text,
    ad_set_id        text,
    ad_id            text,
    created_at       timestamptz         NOT NULL DEFAULT now()
);

CREATE INDEX ON facebook_ad_drafts (organization_id, created_at DESC);
