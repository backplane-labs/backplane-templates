# backplane-templates

Message templates for a [Backplane](https://github.com/backplane-labs/backplane)
installation: one small service that does one job. Templates belong to the
installation — its branding, its wording, its languages — not to the products.
An operator writes and previews them in the console; a product only raises an
event or a hook with data, and the operator wires it to `templates.Render` and
on to a delivery service (`smtp.Send`, push, ...).

## What it offers the installation

| Kind | Name | What |
| --- | --- | --- |
| Activity | `templates.Render` | `{template, locale?, data}` → `{parts, template, version}`: every part of the template's active version rendered with `data`. Failures are final: no template, nothing activated, data missing a field the template uses. |
| Console | `/s/templates/*` | The editor: templates with their parts, sample data with key completion after `{{.`, a live preview rendered by the service's own engine (html in a sandboxed frame), problems marked on the failing line, versions with diff against the draft, activation and rollback. Backed by the internal `templates.console.v1.AdminService`; every change is an audit record (`template.saved`, `template.activated`, `template.deleted`). |

## Templates

A template is a name, a description and parts. A part has a name
(`subject`, `text`, `html`, `title`, `body`, ...), a kind and Go template
source:

- `text` parts use `text/template`: data is inserted as is.
- `html` parts use `html/template`: data is escaped for its HTML context.
- A field the data does not have is an error, not an empty string.
- Functions: `upper`, `lower`, `title`, `trim`, `replace`, `join`, `default`,
  `date` (Go layout, RFC 3339 input), `json`, `add`, `mul`, `round`. Nothing
  reaches the process or the network.

Names are dotted lower-case words (`order.placed`). A locale variant is
`order.placed@ru`; a render asks for a locale and falls back to the
installation's `default_locale` variant, then the bare name. A template whose
name starts with `_` is a shared piece: its part of the same name is
available to every template as `{{template "_footer" .}}`.

Every save is a new version with its sample data and a comment; the first one
is activated, later ones when asked. Renders use the active version, so an
edit is safe until it is activated, and a rollback is activating an older one.

## Configuration

```yaml
default_locale: en             # live: the variant renders fall back to
store:
  dsn: postgres://templates:secret@postgres:5432/backplane?sslmode=disable
```

The store is required: without it the instance is not ready. State lives in
the schema `templates` (`template`, `template_version`), migrated at start.

## Development

```sh
make configure          # linter, sqld and proto generators into ./bin
make configure-web      # the console module's packages (GitHub Packages)
make gen                # internal API: Go stubs and the module's TypeScript client
make test               # unit tests; PostgreSQL tests skip without BACKPLANE_TEST_PG
make test-live          # against platform-in-a-box's PostgreSQL (backplane: make up)
make web                # build the console module (embedded by make build)
make run                # against the local stack with deployments/dev.yaml
make db                 # regenerate internal/store/db from schema.sql and queries
```

The release binary and image carry the console module; a binary built
without `make web` serves the internal API alone and declares no UI.
