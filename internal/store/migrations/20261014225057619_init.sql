-- sqld:up
CREATE SCHEMA IF NOT EXISTS "templates";
CREATE TABLE "templates"."template" (
  "name" text NOT NULL,
  "description" text NOT NULL DEFAULT ''::text,
  "active_version" int8,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  "updated_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("name")
);
CREATE TABLE "templates"."template_version" (
  "name" text NOT NULL,
  "version" int8 NOT NULL,
  "parts" jsonb NOT NULL,
  "translations" jsonb NOT NULL,
  "default_language" text NOT NULL,
  "sample" jsonb NOT NULL DEFAULT '{}'::jsonb,
  "author" text NOT NULL DEFAULT ''::text,
  "comment" text NOT NULL DEFAULT ''::text,
  "created_at" timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY ("name", "version")
);
ALTER TABLE "templates"."template_version" ADD CONSTRAINT "template_version_name_fkey" FOREIGN KEY ("name") REFERENCES "templates"."template" ("name") ON DELETE CASCADE;

-- sqld:down
ALTER TABLE "templates"."template_version" DROP CONSTRAINT "template_version_name_fkey";
DROP TABLE "templates"."template_version";
DROP TABLE "templates"."template";
