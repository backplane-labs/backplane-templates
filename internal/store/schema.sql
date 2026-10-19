-- backplane-templates' state: the installation's templates, every version
-- of each, and which version renders. Content lives in versions; a
-- template row is the name, its description and the active pointer.
CREATE SCHEMA templates;

CREATE TABLE templates.template (
  name           text        PRIMARY KEY,
  description    text        NOT NULL DEFAULT '',
  -- The version Render uses; NULL: none activated yet.
  active_version bigint,
  -- The template's directory under the files source (files.dir) that
  -- keeps it; NULL: the console's. A managed template changes only
  -- through its files.
  managed_by     text,
  created_at     timestamptz NOT NULL DEFAULT now(),
  updated_at     timestamptz NOT NULL DEFAULT now()
);

-- A version is immutable: a save is a new one, every language at once.
-- parts is the structure every language shares: [{name, kind}];
-- translations is {language: {part: source}}; default_language renders
-- when the asked language and its base are missing; sample is the data
-- the console previews with.
CREATE TABLE templates.template_version (
  name             text        NOT NULL REFERENCES templates.template (name) ON DELETE CASCADE,
  version          bigint      NOT NULL,
  parts            jsonb       NOT NULL,
  translations     jsonb       NOT NULL,
  default_language text        NOT NULL,
  sample           jsonb       NOT NULL DEFAULT '{}'::jsonb,
  author           text        NOT NULL DEFAULT '',
  comment          text        NOT NULL DEFAULT '',
  created_at       timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (name, version)
);
