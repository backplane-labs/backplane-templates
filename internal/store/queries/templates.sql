-- Templates (internal/templates, internal/admin).

-- name: ListTemplates :many
SELECT t.name, t.description, t.active_version, t.created_at, t.updated_at,
  (SELECT count(*) FROM templates.template_version v WHERE v.name = t.name) AS versions,
  (SELECT v.parts FROM templates.template_version v WHERE v.name = t.name AND v.version = t.active_version) AS active_parts,
  (SELECT v.translations FROM templates.template_version v WHERE v.name = t.name AND v.version = t.active_version) AS active_translations,
  (SELECT v.default_language FROM templates.template_version v WHERE v.name = t.name AND v.version = t.active_version) AS active_default
FROM templates.template t
ORDER BY t.name;

-- name: GetTemplate :one
SELECT name, description, active_version, created_at, updated_at FROM templates.template WHERE name = @name;

-- name: UpsertTemplate :exec
INSERT INTO templates.template (name, description)
VALUES (@name, @description)
ON CONFLICT (name) DO UPDATE SET description = EXCLUDED.description, updated_at = now();

-- name: NextVersion :one
SELECT coalesce(max(version), 0) + 1 AS version FROM templates.template_version WHERE name = @name;

-- name: InsertVersion :exec
INSERT INTO templates.template_version (name, version, parts, translations, default_language, sample, author, comment)
VALUES (@name, @version, @parts, @translations, @default_language, @sample, @author, @comment);

-- name: ActivateVersion :execrows
UPDATE templates.template t SET active_version = @version, updated_at = now()
WHERE t.name = @name AND EXISTS (SELECT 1 FROM templates.template_version v WHERE v.name = t.name AND v.version = @version);

-- name: DeleteTemplate :execrows
DELETE FROM templates.template WHERE name = @name;

-- name: ListVersions :many
SELECT version, author, comment, created_at FROM templates.template_version
WHERE name = @name ORDER BY version DESC;

-- name: GetVersion :one
SELECT name, version, parts, translations, default_language, sample, author, comment, created_at
FROM templates.template_version
WHERE name = @name AND version = @version;

-- name: GetActiveVersion :one
SELECT v.name, v.version, v.parts, v.translations, v.default_language FROM templates.template t
JOIN templates.template_version v ON v.name = t.name AND v.version = t.active_version
WHERE t.name = @name;

-- The active versions of the shared pieces (names starting with "_"),
-- for partials and the cache key.

-- name: ListActivePartials :many
SELECT v.name, v.version, v.parts, v.translations, v.default_language FROM templates.template t
JOIN templates.template_version v ON v.name = t.name AND v.version = t.active_version
WHERE t.name LIKE '\_%' ORDER BY t.name;

-- name: ActiveVersions :many
SELECT name, active_version FROM templates.template WHERE name = @name OR name LIKE '\_%';
