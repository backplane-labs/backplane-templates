import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import type { ClientRuntime, ClientConstructor, ClientState } from '@backplane-labs/backplane-client';
import {
  ActivateVersionResponseSchema, DeleteTemplateResponseSchema, GetSettingsResponseSchema, GetTemplateResponseSchema, ListTemplatesResponseSchema, PreviewResponseSchema,
  SaveTemplateResponseSchema, type GetTemplateRequest, type PreviewRequest, type SaveTemplateRequest,
} from './gen/proto/templates/console/v1/admin_pb';

const state: ClientState = { connection: 'connected', session: null, connectionId: 1 };
const at = (minutes: number) => timestampFromDate(new Date(Date.UTC(2026, 9, 15, 12, minutes)));
const parts = [{ name: 'subject', kind: 'text' }, { name: 'text', kind: 'text' }, { name: 'html', kind: 'html' }];
const translations = [
  { language: 'en', sources: { subject: 'Order {{.order.id}} for {{.name}}', text: 'Hi {{.name}},\nyour order {{.order.id}} is on its way.', html: '<p>Hi <b>{{.name}}</b>,</p><p>your order {{.order.id}} is on its way.</p>' } },
  { language: 'ru', sources: { subject: 'Заказ {{.order.id}} для {{.name}}', text: '', html: '<p>Здравствуйте, <b>{{.name}}</b>!</p>' } },
];
let versions = 2;
/** Explicit local fixture of AdminService: renders with a naive substitution. Replace with BackplaneClient or your own adapter. */
export const fixtureClient: ClientRuntime = {
  getSnapshot: () => state, subscribe: () => () => {},
  client<T extends object>(constructor: ClientConstructor<T>): T {
    if (!('listTemplates' in constructor.prototype)) throw new Error(`No fixture for ${constructor.name}`);
    const render = (source: string, data: unknown) => source.replace(/\{\{\s*\.([\w.]+)\s*\}\}/g, (_, path: string) => String(path.split('.').reduce<unknown>((v, key) => (v as Record<string, unknown>)?.[key], data) ?? ''));
    return {
      async getSettings() { return create(GetSettingsResponseSchema, { languages: ['en', 'ru', 'de'], defaultLanguage: 'en' }); },
      async listTemplates() { return create(ListTemplatesResponseSchema, { templates: [
        { name: '_footer', description: 'Shared footer', activeVersion: 1n, versions: 1n, parts: ['html'], languages: ['en', 'ru'], defaultLanguage: 'en', updatedAt: at(0) },
        { name: 'order.placed', description: 'Order confirmation', activeVersion: 2n, versions: BigInt(versions), parts: ['subject', 'text', 'html'], languages: ['en', 'ru'], defaultLanguage: 'en', incomplete: ['ru'], updatedAt: at(30) },
      ] }); },
      async getTemplate(request: GetTemplateRequest) { return create(GetTemplateResponseSchema, { name: request.name, description: 'Order confirmation', activeVersion: 2n,
        versions: [{ version: 2n, author: 'console', comment: 'russian', createdAt: at(30) }, { version: 1n, author: 'console', comment: 'initial', createdAt: at(0) }],
        content: { version: request.version || 2n, parts, translations: request.version === 1n ? translations.slice(0, 1) : translations, defaultLanguage: 'en',
          sample: '{"name": "Ann", "order": {"id": 42}}', author: 'console', comment: request.version === 1n ? 'initial' : 'russian', createdAt: at(30) } }); },
      async saveTemplate(request: SaveTemplateRequest) { versions += 1; return create(SaveTemplateResponseSchema, { version: BigInt(versions), activated: request.activate,
        problems: request.sample.includes('"name"') ? [] : [{ language: request.defaultLanguage, part: 'subject', message: 'map has no entry for key "name"' }] }); },
      async activateVersion() { return create(ActivateVersionResponseSchema); },
      async deleteTemplate() { return create(DeleteTemplateResponseSchema, { deleted: true }); },
      async preview(request: PreviewRequest) { let data: unknown = {}; try { data = JSON.parse(request.data || '{}'); } catch { /* fixture */ }
        const sources = request.translation?.sources ?? {};
        return create(PreviewResponseSchema, { parts: Object.fromEntries(request.parts.map((p) => [p.name, render(sources[p.name] ?? '', data)])), problems: [] }); },
    } as unknown as T;
  },
};
