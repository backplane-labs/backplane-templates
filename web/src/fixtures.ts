import type { ClientConstructor, ClientRuntime, ClientState } from '@backplane-labs/backplane-client';
import { create, type JsonObject, type JsonValue, type Message, toJson } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import { type AnyResponse, type Interceptor, type UnaryRequest, WsTransport } from '@gopherex/ws-proto-transport';
import {
  ActivateVersionResponseSchema,
  DeleteTemplateResponseSchema,
  GetSettingsResponseSchema,
  GetTemplateResponseSchema,
  ListTemplatesResponseSchema,
  PreviewResponseSchema,
  SaveTemplateResponseSchema,
} from './gen/proto/templates/console/v1/admin_pb';

const state: ClientState = { connection: 'connected', session: null, connectionId: 1 };
const at = (minutes: number) => timestampFromDate(new Date(Date.UTC(2026, 9, 15, 12, minutes)));
const parts = [
  { name: 'subject', kind: 'text' },
  { name: 'text', kind: 'text' },
  { name: 'html', kind: 'html' },
];
const translations = [
  {
    language: 'en',
    sources: {
      subject: 'Order {{.order.id}} for {{.name}}',
      text: 'Hi {{.name}},\nyour order {{.order.id}} is on its way.',
      html: '<p>Hi <b>{{.name}}</b>,</p><p>your order {{.order.id}} is on its way.</p>',
    },
  },
  {
    language: 'ru',
    sources: { subject: 'Заказ {{.order.id}} для {{.name}}', text: '', html: '<p>Здравствуйте, <b>{{.name}}</b>!</p>' },
  },
];
let versions = 2;

/** The request's JSON form, the fixture's typed view of the wire value. */
function json(request: UnaryRequest): JsonObject {
  const value: JsonValue = toJson(request.method.input, request.message);
  return typeof value === 'object' && value !== null && !Array.isArray(value) ? value : {};
}
function stringField(value: JsonObject, key: string): string {
  const field = value[key];
  return typeof field === 'string' ? field : '';
}
function booleanField(value: JsonObject, key: string): boolean {
  return value[key] === true;
}
/** 64-bit fields serialize as decimal strings in the proto JSON form. */
function bigField(value: JsonObject, key: string): bigint {
  const field = value[key];
  if (typeof field === 'number') return BigInt(field);
  if (typeof field === 'string' && field !== '') return BigInt(field);
  return 0n;
}
/** Parses the preview's sample data; malformed JSON renders against an empty object. */
function parseJson(text: string): JsonValue {
  try {
    return JSON.parse(text);
  } catch {
    /* fixture */
  }
  return {};
}
function lookup(data: JsonValue, path: string): string {
  let value: JsonValue = data;
  for (const key of path.split('.')) {
    if (typeof value !== 'object' || value === null || Array.isArray(value)) return '';
    const next: JsonValue | undefined = value[key];
    if (next === undefined) return '';
    value = next;
  }
  return String(value);
}
/** Renders with a naive substitution, the fixture's stand-in for the service's renderer. */
const render = (source: string, data: JsonValue) =>
  source.replace(/\{\{\s*\.([\w.]+)\s*\}\}/g, (_, path: string) => lookup(data, path));

function respond(request: UnaryRequest, message: Message): AnyResponse {
  return { stream: false, method: request.method, header: request.header, message, trailer: {} };
}

/** Local answers per RPC; the generated AdminServiceClient routes through them. */
const answers = new Map<string, (request: UnaryRequest) => Promise<AnyResponse>>([
  [
    'GetSettings',
    (request) =>
      Promise.resolve(
        respond(request, create(GetSettingsResponseSchema, { languages: ['en', 'ru', 'de'], defaultLanguage: 'en' })),
      ),
  ],
  [
    'ListTemplates',
    (request) =>
      Promise.resolve(
        respond(
          request,
          create(ListTemplatesResponseSchema, {
            templates: [
              {
                name: '_footer',
                description: 'Shared footer',
                activeVersion: 1n,
                versions: 1n,
                parts: ['html'],
                languages: ['en', 'ru'],
                defaultLanguage: 'en',
                updatedAt: at(0),
              },
              {
                name: 'order.placed',
                description: 'Order confirmation',
                activeVersion: 2n,
                versions: BigInt(versions),
                parts: ['subject', 'text', 'html'],
                languages: ['en', 'ru'],
                defaultLanguage: 'en',
                incomplete: ['ru'],
                updatedAt: at(30),
              },
            ],
          }),
        ),
      ),
  ],
  [
    'GetTemplate',
    (request) => {
      const value = json(request),
        version = bigField(value, 'version');
      return Promise.resolve(
        respond(
          request,
          create(GetTemplateResponseSchema, {
            name: stringField(value, 'name'),
            description: 'Order confirmation',
            activeVersion: 2n,
            versions: [
              { version: 2n, author: 'console', comment: 'russian', createdAt: at(30) },
              { version: 1n, author: 'console', comment: 'initial', createdAt: at(0) },
            ],
            content: {
              version: version || 2n,
              parts,
              translations: version === 1n ? translations.slice(0, 1) : translations,
              defaultLanguage: 'en',
              sample: '{"name": "Ann", "order": {"id": 42}}',
              author: 'console',
              comment: version === 1n ? 'initial' : 'russian',
              createdAt: at(30),
            },
          }),
        ),
      );
    },
  ],
  [
    'SaveTemplate',
    (request) => {
      const value = json(request);
      versions += 1;
      return Promise.resolve(
        respond(
          request,
          create(SaveTemplateResponseSchema, {
            version: BigInt(versions),
            activated: booleanField(value, 'activate'),
            problems: stringField(value, 'sample').includes('"name"')
              ? []
              : [
                  {
                    language: stringField(value, 'defaultLanguage'),
                    part: 'subject',
                    message: 'map has no entry for key "name"',
                  },
                ],
          }),
        ),
      );
    },
  ],
  ['ActivateVersion', (request) => Promise.resolve(respond(request, create(ActivateVersionResponseSchema)))],
  [
    'DeleteTemplate',
    (request) => Promise.resolve(respond(request, create(DeleteTemplateResponseSchema, { deleted: true }))),
  ],
  [
    'Preview',
    (request) => {
      const value = json(request),
        sources = value.translation ?? {},
        sourceOf = (name: string) =>
          typeof sources === 'object' && !Array.isArray(sources) ? stringField(sources, name) : '',
        data = parseJson(stringField(value, 'data') || '{}'),
        listed = Array.isArray(value.parts) ? value.parts : [];
      return Promise.resolve(
        respond(
          request,
          create(PreviewResponseSchema, {
            parts: Object.fromEntries(
              listed.map((part) => {
                const name =
                  typeof part === 'object' && part !== null && !Array.isArray(part) ? stringField(part, 'name') : '';
                return [name, render(sourceOf(name), data)];
              }),
            ),
            problems: [],
          }),
        ),
      );
    },
  ],
]);

/** Answers AdminService calls locally; never dials (paused, every known method short-circuits). */
const answerFixture: Interceptor = (_next) => (request) => {
  const answer = answers.get(request.method.name);
  if (answer === undefined || request.stream) throw new Error(`No fixture for ${request.method.name}`);
  return answer(request);
};

const fixtureTransport = new WsTransport('wss://fixture.invalid/', { paused: true, interceptors: [answerFixture] });

/** Explicit local fixture of AdminService: renders with a naive substitution. Replace with BackplaneClient or your own adapter. */
export const fixtureClient: ClientRuntime = {
  getSnapshot: () => state,
  subscribe: () => () => {},
  client<T extends object>(service: ClientConstructor<T>): T {
    if (!('listTemplates' in service.prototype)) throw new Error(`No fixture for ${service.name}`);
    return new service(fixtureTransport);
  },
};
