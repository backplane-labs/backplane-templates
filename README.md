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
| Activity | `templates.Render` | `{template, language?, data}` → `{parts, language, version}`: every part of the template's active version rendered in the language with `data`. Failures are final: no template, nothing activated, no usable language, data missing a field the template uses. |
| Console | `/s/templates/*` | The editor: templates with their languages and parts, sample data with key completion after `{{.`, a live preview of the language on screen rendered by the service's own engine (html in a sandboxed frame), untranslated parts and problems marked, versions with a per-language diff against the draft, activation and rollback. Backed by the internal `templates.console.v1.AdminService`; every change is an audit record (`template.saved`, `template.activated`, `template.deleted`). |

## Templates

A template is a name, a description, parts and languages. A part has a name
(`subject`, `text`, `html`, `title`, `body`, ...) and a kind; every language
gives a Go template source for every part:

- `text` parts use `text/template`: data is inserted as is.
- `html` parts use `html/template`: data is escaped for its HTML context.
- A field the data does not have is an error, not an empty string.
- Functions: `upper`, `lower`, `title`, `trim`, `replace`, `join`, `default`,
  `json`, `add`, `mul`, `round`, `date LAYOUT t`. By language:
  `plural n "one" "other"` (or `"one" "few" "many"`, CLDR rules: 21 товар,
  22 товара, 25 товаров), `number n [digits]` (1,234.50 / 1 234,50),
  `longdate t` (October 15, 2026 / 15 октября 2026), `monthname t`,
  `weekday t`. Nothing reaches the process or the network.

Languages are BCP 47 tags (`en`, `ru`, `pt-BR`). A render asks for one and
gets the closest: the tag, its base language, the template's default
language, the installation's `default_language`. One version holds every
language, so a save, an activation and a rollback move them together.

A template whose name starts with `_` is a shared piece: its part of the same
name is available to every template as `{{template "_footer" .}}`, in the
language being rendered.

Every save is a new version with its sample data and a comment; the first one
is activated, later ones when asked. Renders use the active version, so an
edit is safe until it is activated, and a rollback is activating an older one.

### What a template cannot do

A Go template cannot be interrupted, so the engine makes an unbounded loop
impossible to write. A template holds no integers: number literals compile to
floats, `len`, `index` and `slice` work on floats, data numbers are floats,
and `range` refuses a float, so a template loops only over data. On top of
that, `range` iterates only over a field or a variable holding data, ranges
nest at most two deep (counted through shared pieces), templates define no
templates of their own and shared pieces include each other without cycles.
Data holds at most 10000 elements, a part renders at most 1 MiB (`replace`
and `join` refuse to build more), a render takes at most 2 s, and the
renders running at once are bounded.

## Configuration

```yaml
languages: [en, ru]            # live: the languages the editor offers
default_language: en           # live: the language renders fall back to last
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
