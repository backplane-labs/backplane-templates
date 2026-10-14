import { create } from '@bufbuild/protobuf';
import { timestampFromDate } from '@bufbuild/protobuf/wkt';
import type { ClientRuntime, ClientConstructor, ClientState } from '@backplane-labs/backplane-client';
import {
  ActivateVersionResponseSchema, DeleteTemplateResponseSchema, GetTemplateResponseSchema, ListTemplatesResponseSchema, PreviewResponseSchema, SaveTemplateResponseSchema,
  type GetTemplateRequest, type PreviewRequest, type SaveTemplateRequest,
} from './gen/proto/templates/console/v1/admin_pb';

const state: ClientState = { connection: 'connected', session: null, connectionId: 1 };
const at = (minutes: number) => timestampFromDate(new Date(Date.UTC(2026, 9, 15, 12, minutes)));
const parts = [
  { name: 'subject', kind: 'text', source: 'Order {{.order.id}} for {{.name}}' },
  { name: 'text', kind: 'text', source: 'Hi {{.name}},\nyour order {{.order.id}} is on its way.\n{{template "_footer" .}}' },
  { name: 'html', kind: 'html', source: '<p>Hi <b>{{.name}}</b>,</p><p>your order {{.order.id}} is on its way.</p>{{template "_footer" .}}' },
];
let versions = 2;
/** Explicit local fixture of AdminService: renders with a naive substitution. Replace with BackplaneClient or your own adapter. */
export const fixtureClient: ClientRuntime = {
  getSnapshot: () => state, subscribe: () => () => {},
  client<T extends object>(constructor: ClientConstructor<T>): T {
    if (!('listTemplates' in constructor.prototype)) throw new Error(`No fixture for ${constructor.name}`);
    const render = (source: string, data: unknown) => source.replace(/\{\{\s*\.([\w.]+)\s*\}\}/g, (_, path: string) => String(path.split('.').reduce<unknown>((v, key) => (v as Record<string, unknown>)?.[key], data) ?? '')).replace(/\{\{template "_footer" \.\}\}/g, '-- Shop');
    return {
      async listTemplates() { return create(ListTemplatesResponseSchema, { templates: [
        { name: '_footer', description: 'Shared footer', activeVersion: 1n, versions: 1n, parts: ['text', 'html'], updatedAt: at(0) },
        { name: 'order.placed', description: 'Order confirmation', activeVersion: 2n, versions: BigInt(versions), parts: ['subject', 'text', 'html'], updatedAt: at(30) },
        { name: 'order.placed@ru', description: '', activeVersion: 0n, versions: 1n, parts: ['subject', 'text'], updatedAt: at(10) },
      ] }); },
      async getTemplate(request: GetTemplateRequest) { return create(GetTemplateResponseSchema, { name: request.name, description: 'Order confirmation', activeVersion: 2n,
        versions: [{ version: 2n, author: 'console', comment: 'html part', createdAt: at(30) }, { version: 1n, author: 'console', comment: 'initial', createdAt: at(0) }],
        content: { version: request.version || 2n, parts: request.version === 1n ? parts.slice(0, 2) : parts, sample: '{"name": "Ann", "order": {"id": 42}}', author: 'console', comment: request.version === 1n ? 'initial' : 'html part', createdAt: at(30) } }); },
      async saveTemplate(request: SaveTemplateRequest) { versions += 1; return create(SaveTemplateResponseSchema, { version: BigInt(versions), activated: request.activate, problems: request.sample.includes('"name"') ? [] : [{ part: 'subject', message: 'map has no entry for key "name"' }] }); },
      async activateVersion() { return create(ActivateVersionResponseSchema); },
      async deleteTemplate() { return create(DeleteTemplateResponseSchema, { deleted: true }); },
      async preview(request: PreviewRequest) { let data: unknown = {}; try { data = JSON.parse(request.data || '{}'); } catch { /* fixture */ }
        return create(PreviewResponseSchema, { parts: Object.fromEntries(request.parts.map((p) => [p.name, render(p.source, data)])), problems: request.parts.some((p) => p.source.includes('{{.missing}}')) ? [{ part: 'text', message: 'map has no entry for key "missing"' }] : [] }); },
    } as unknown as T;
  },
};
