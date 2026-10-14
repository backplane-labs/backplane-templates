-- backplane-templates' state: the installation's templates, every version
-- of each, and which version renders. Content lives in versions; a
-- template row is the name, its description and the active pointer.
CREATE SCHEMA templates;

CREATE TABLE templates.template (
  name           text        PRIMARY KEY,
  description    text        NOT NULL DEFAULT '',
  -- The version Render uses; NULL: none activated yet.
  active_version bigint,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- A version is immutable: a save is a new one. parts is the list of
-- {name, kind, source}; sample is the data the console previews with.
CREATE TABLE templates.template_version (
  name       text        NOT NULL REFERENCES templates.template (name) ON DELETE CASCADE,
  version    bigint      NOT NULL,
  parts      jsonb       NOT NULL,
  sample     jsonb       NOT NULL DEFAULT '{}'::jsonb,
  author     text        NOT NULL DEFAULT '',
  comment    text        NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (name, version)
);
