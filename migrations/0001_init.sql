-- Initial schema. Vector dimension (1024) locked by ml/spikes/embedding_benchmark_report.md
-- (intfloat/multilingual-e5-large). See plan.md §3.

CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto; -- gen_random_uuid()

CREATE TABLE cases (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                text NOT NULL,
    status              text NOT NULL DEFAULT 'intake',
    detected_case_type  text,
    typology_confidence text,
    created_at          timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE documents (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    case_id      uuid NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
    filename     text NOT NULL,
    sha256       text NOT NULL,
    parse_status text NOT NULL DEFAULT 'pending',
    object_ref   text, -- pointer to LiteParse output (text + bounding boxes) in object storage
    created_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (case_id, sha256)
);

CREATE TABLE typologies (
    id                      uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name                    text NOT NULL UNIQUE,
    description             text,
    discriminating_features jsonb NOT NULL DEFAULT '[]',
    exemplar_ruling_ids     uuid[] NOT NULL DEFAULT '{}'
);

CREATE TABLE rulings (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    external_id   text NOT NULL UNIQUE,
    full_text     text NOT NULL,
    case_type     text,
    outcome       text NOT NULL DEFAULT 'pending' CHECK (outcome IN ('upheld', 'reverted', 'pending')),
    revert_reason text,
    court         text,
    date          date,
    tags          jsonb NOT NULL DEFAULT '{}',
    embedding     vector(1024),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX rulings_embedding_hnsw ON rulings USING hnsw (embedding vector_cosine_ops);
CREATE INDEX rulings_outcome_idx ON rulings (outcome);
CREATE INDEX rulings_case_type_idx ON rulings (case_type);

CREATE TABLE chunks (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    document_id   uuid NOT NULL REFERENCES documents(id) ON DELETE CASCADE,
    text          text NOT NULL,
    section_label text,
    embedding     vector(1024),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX chunks_embedding_hnsw ON chunks USING hnsw (embedding vector_cosine_ops);
CREATE INDEX chunks_document_id_idx ON chunks (document_id);

CREATE TABLE packages (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    case_id      uuid REFERENCES cases(id) ON DELETE CASCADE,
    use_case     text NOT NULL CHECK (use_case IN ('classify', 'draft', 'risk_explain', 'similar_explain')),
    prompt_version int NOT NULL,
    status       text NOT NULL DEFAULT 'draft'
                 CHECK (status IN ('draft', 'ready', 'submitted', 'completed', 'failed', 'cancelled')),
    bundle       jsonb NOT NULL, -- manifest/context/schema; packed archive ref lives alongside
    created_by   text,
    submitted_at timestamptz,
    completed_at timestamptz,
    error        text,
    retry_of     uuid REFERENCES packages(id),
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX packages_case_id_idx ON packages (case_id);
CREATE INDEX packages_status_idx ON packages (status);

CREATE TABLE package_results (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    package_id        uuid NOT NULL REFERENCES packages(id) ON DELETE CASCADE,
    raw_response      jsonb NOT NULL,
    validated_payload jsonb,
    validation_status text NOT NULL DEFAULT 'pending' CHECK (validation_status IN ('pending', 'valid', 'invalid')),
    received_at       timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE drafts (
    id                uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    case_id           uuid NOT NULL REFERENCES cases(id) ON DELETE CASCADE,
    package_id        uuid REFERENCES packages(id),
    generated_text    text NOT NULL,
    cited_ruling_ids  uuid[] NOT NULL DEFAULT '{}',
    prompt_version    int,
    edit_history      jsonb NOT NULL DEFAULT '[]',
    created_at        timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE risk_reports (
    id              uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    case_id         uuid REFERENCES cases(id) ON DELETE CASCADE,
    draft_id        uuid REFERENCES drafts(id),
    risk_grade      text CHECK (risk_grade IN ('low', 'medium', 'high')),
    neighbor_rulings jsonb NOT NULL DEFAULT '[]', -- [{ruling_id, similarity}]
    explanation_package_id uuid REFERENCES packages(id),
    created_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE audit_log (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    actor      text NOT NULL,
    action     text NOT NULL,
    entity     text NOT NULL,
    entity_id  uuid,
    metadata   jsonb NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_log_entity_idx ON audit_log (entity, entity_id);
