import { useEffect, useMemo, useState } from 'react';
import { useParams } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { create } from '@bufbuild/protobuf';
import { timestampDate, type Timestamp } from '@bufbuild/protobuf/wkt';
import { useClient } from '@backplane-labs/backplane-react';
import { definePlugin, usePluginContext, usePluginNavigate } from '@backplane-labs/backplane-plugin-sdk';
import { ConnectionNotice, usePlatformAction, usePlatformQuery } from '@backplane-labs/backplane-platform-ui';
import { CodeEditor, DiffViewer, type EditorCompletionResult, type EditorDiagnostic } from '@backplane-labs/backplane-editors';
import {
  Badge, Button, Checkbox, ConfirmAction, EmptyState, Field, FieldDescription, FieldLabel, Input, Label, PageHeader, Panel, SelectControl, Skeleton, Stack, StatusBadge,
  Table, TableBody, TableCell, TableHead, TableHeader, TableRow, Timestamp as Time,
} from '@backplane-labs/backplane-ui';
import { AdminServiceClient } from './gen/proto/templates/console/v1/admin_ws_pb';
import {
  ActivateVersionRequestSchema, DeleteTemplateRequestSchema, GetTemplateRequestSchema, ListTemplatesRequestSchema, PartSchema, PreviewRequestSchema, SaveTemplateRequestSchema,
  type GetTemplateResponse, type Part, type Problem,
} from './gen/proto/templates/console/v1/admin_pb';
import navigation from './navigation';

const date = (value?: Timestamp) => value ? timestampDate(value) : undefined;
const mono = { fontFamily: 'var(--font-mono)' } as const;
const muted = { color: 'var(--muted-foreground)', fontSize: 12 } as const;
const namePattern = /^_?[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*(?:@[a-z]{2}(?:-[A-Za-z]{2,4})?)?$/;
const partPattern = /^[a-z][a-z0-9_]*$/;
const starter: Part[] = [
  create(PartSchema, { name: 'subject', kind: 'text', source: '' }),
  create(PartSchema, { name: 'text', kind: 'text', source: '' }),
  create(PartSchema, { name: 'html', kind: 'html', source: '' }),
];
function useText() { return useTranslation('module.templates').t; }
/** JSON text in one canonical form, for comparing drafts; invalid text as is. */
function canonical(text: string) { try { return JSON.stringify(JSON.parse(text || '{}')); } catch { return text; } }
type Notice = { tone: 'success' | 'warning' | 'danger'; text: string };
const route = (name: string) => `edit/${encodeURIComponent(name)}`;

/** Every template of the installation. */
function List() {
  const t = useText(), navigate = usePluginNavigate(), client = useClient(AdminServiceClient), context = usePluginContext();
  const state = usePlatformQuery('templates:list', (signal) => client.listTemplates(create(ListTemplatesRequestSchema), { signal }), { keep: true });
  return <Stack gap={16}><ConnectionNotice />
    <PageHeader title={t('title')} description={t('description')} meta={<Badge variant="outline">{t(context.environment)}</Badge>}
      actions={<Stack direction="row" gap={8}><Button variant="outline" size="sm" onClick={state.refresh}>{t('refresh')}</Button><Button size="sm" onClick={() => navigate('new')}>{t('newTemplate')}</Button></Stack>} />
    <Panel title={t('templates')} count={state.value?.templates.length} flush>
      {state.error !== undefined ? <EmptyState title={t('error')} role="alert" /> : !state.value
        ? <Stack gap={8} style={{ padding: 12 }} role="status" aria-label={t('loading')}><Skeleton style={{ height: 28 }} /><Skeleton style={{ height: 28 }} /></Stack>
        : !state.value.templates.length ? <EmptyState title={t('noTemplates')} action={<Button size="sm" onClick={() => navigate('new')}>{t('newTemplate')}</Button>} />
        : <Table><TableHeader><TableRow><TableHead>{t('name')}</TableHead><TableHead>{t('descriptionLabel')}</TableHead><TableHead>{t('parts')}</TableHead><TableHead>{t('active')}</TableHead><TableHead>{t('versions')}</TableHead><TableHead>{t('updated')}</TableHead></TableRow></TableHeader>
          <TableBody>{state.value.templates.map((tpl) => <TableRow key={tpl.name} tabIndex={0} style={{ cursor: 'pointer' }} onClick={() => navigate(route(tpl.name))}
            onKeyDown={(event) => { if (event.key === 'Enter') navigate(route(tpl.name)); }}>
            <TableCell style={mono}>{tpl.name}</TableCell>
            <TableCell>{tpl.description || <span style={muted}>—</span>}</TableCell>
            <TableCell><Stack direction="row" gap={4}>{tpl.parts.map((part) => <Badge key={part} variant="outline">{part}</Badge>)}</Stack></TableCell>
            <TableCell>{tpl.activeVersion ? <StatusBadge tone="success">v{String(tpl.activeVersion)}</StatusBadge> : <StatusBadge tone="warning">{t('notActivated')}</StatusBadge>}</TableCell>
            <TableCell>{String(tpl.versions)}</TableCell>
            <TableCell><Time value={date(tpl.updatedAt)} /></TableCell>
          </TableRow>)}</TableBody></Table>}
    </Panel>
  </Stack>;
}

/** Keys of the sample under the cursor's path, for {{. completion. */
function sampleKeys(sample: string, path: string[]): string[] {
  try {
    let value: unknown = JSON.parse(sample || '{}');
    for (const key of path) value = (value as Record<string, unknown>)?.[key];
    return value && typeof value === 'object' && !Array.isArray(value) ? Object.keys(value) : [];
  } catch { return []; }
}
function complete(sample: string) {
  return async ({ value, position }: { value: string; position: number }): Promise<EditorCompletionResult | null> => {
    const before = value.slice(0, position), open = before.lastIndexOf('{{');
    if (open < 0 || before.lastIndexOf('}}') > open) return null;
    const match = /(?:^|[\s(|])\$?\.([\w.]*)$/.exec(before.slice(open + 2));
    if (!match) return null;
    const segments = match[1].split('.'), word = segments.pop() ?? '';
    return { from: position - word.length, options: sampleKeys(sample, segments).map((key) => ({ label: key, type: 'property' as const })) };
  };
}

/** Debounced value: the preview runs after typing settles. */
function useSettled<T>(value: T, ms = 400) {
  const [settled, setSettled] = useState(value);
  useEffect(() => { const id = setTimeout(() => setSettled(value), ms); return () => clearTimeout(id); }, [value, ms]);
  return settled;
}

/** The editor: parts, sample, live preview, versions. */
function Editor() {
  const t = useText(), navigate = usePluginNavigate(), client = useClient(AdminServiceClient), { mode } = usePluginContext();
  const params = useParams(), existing = params.name ? decodeURIComponent(params.name) : '';
  const [version, setVersion] = useState(0n), [notice, setNotice] = useState<Notice>();
  const state = usePlatformQuery(`templates:get:${existing}:${version}`, (signal) => existing
    ? client.getTemplate(create(GetTemplateRequestSchema, { name: existing, version }), { signal }) : Promise.resolve(undefined), { keep: true });
  if (existing && state.error !== undefined) return <EmptyState title={t('error')} role="alert" action={<Button size="sm" variant="outline" onClick={() => navigate('')}>{t('back')}</Button>} />;
  if (existing && !state.value) return <Stack gap={8} role="status" aria-label={t('loading')}><Skeleton style={{ height: 40 }} /><Skeleton style={{ height: 320 }} /></Stack>;
  return <Draft key={`${existing}:${state.value?.content?.version ?? 0n}`} existing={existing} template={state.value} mode={mode} notice={notice} onNotice={setNotice}
    onReload={(next) => { setVersion(next ?? 0n); state.refresh(); }} onOpen={(name) => navigate(route(name))} onBack={() => navigate('')} client={client} />;
}

type Client = ReturnType<typeof useClient<InstanceType<typeof AdminServiceClient>>>;
function Draft({ existing, template, mode, notice, onNotice: setNotice, onReload, onOpen, onBack, client }: {
  existing: string; template?: GetTemplateResponse; mode: 'dark' | 'light'; notice?: Notice; onNotice: (notice?: Notice) => void;
  onReload: (version?: bigint) => void; onOpen: (name: string) => void; onBack: () => void; client: Client;
}) {
  const t = useText();
  const loaded = template?.content;
  const [name, setName] = useState(existing), [description, setDescription] = useState(template?.description ?? '');
  const [parts, setParts] = useState<Part[]>(() => loaded?.parts.length ? loaded.parts.map((p) => create(PartSchema, p)) : starter.map((p) => create(PartSchema, p)));
  const [sample, setSample] = useState(() => loaded?.sample && loaded.sample !== '{}' ? JSON.stringify(JSON.parse(loaded.sample), null, 2) : '{\n  \n}');
  const [selected, setSelected] = useState(parts[0]?.name ?? ''), [comment, setComment] = useState(''), [activate, setActivate] = useState(!existing);
  const [newPart, setNewPart] = useState('');
  const [diffOf, setDiffOf] = useState<bigint>();
  const save = usePlatformAction<void>();
  const baseline = useMemo(() => JSON.stringify({ d: template?.description ?? '', p: loaded?.parts.map((p) => [p.name, p.kind, p.source]) ?? starter.map((p) => [p.name, p.kind, p.source]), s: canonical(loaded?.sample ?? '') }), [template, loaded]);
  const dirty = JSON.stringify({ d: description, p: parts.map((p) => [p.name, p.kind, p.source]), s: canonical(sample) }) !== baseline && (!!existing || parts.some((p) => p.source) || !!name);
  const current = parts.find((p) => p.name === selected) ?? parts[0];
  const sampleValid = useMemo(() => { try { const v = JSON.parse(sample || '{}'); return !!v && typeof v === 'object' && !Array.isArray(v); } catch { return false; } }, [sample]);
  const nameValid = namePattern.test(name);

  // Live preview of the draft, rendered by the service's own engine.
  const settled = useSettled(JSON.stringify({ parts: parts.map((p) => [p.name, p.kind, p.source]), sample }));
  const preview = usePlatformQuery(`templates:preview:${settled}`, (signal) => {
    const draft = JSON.parse(settled) as { parts: [string, string, string][]; sample: string };
    let data = draft.sample; try { JSON.parse(data || '{}'); } catch { data = '{}'; }
    return client.preview(create(PreviewRequestSchema, { parts: draft.parts.map(([n, k, s]) => create(PartSchema, { name: n, kind: k, source: s })), data }), { signal });
  }, { keep: true });
  const problems: Problem[] = preview.value?.problems ?? [];
  const diagnostics: EditorDiagnostic[] = problems.filter((p) => p.part === current?.name).map((p) => {
    const line = /:(\d+):/.exec(p.message)?.[1], source = current?.source ?? '';
    let from = 0;
    if (line) for (let i = 1, n = Number(line); i < n; i++) from = source.indexOf('\n', from) + 1;
    const to = line ? (source.indexOf('\n', from) < 0 ? source.length : source.indexOf('\n', from)) : 0;
    return { from, to, severity: 'error', message: p.message };
  });

  useEffect(() => {
    if (!dirty) return;
    const listener = (event: BeforeUnloadEvent) => { event.preventDefault(); event.returnValue = ''; };
    window.addEventListener('beforeunload', listener); return () => window.removeEventListener('beforeunload', listener);
  }, [dirty]);

  const update = (part: string, patch: Partial<Part>) => setParts((old) => old.map((p) => p.name === part ? create(PartSchema, { ...p, ...patch }) : p));
  const canSave = nameValid && sampleValid && parts.length > 0 && !save.pending && !save.disabled && (dirty || !existing);
  const doSave = () => void save.run(async (signal) => {
    setNotice(undefined);
    try {
      const response = await client.saveTemplate(create(SaveTemplateRequestSchema, { name, description, parts, sample, comment, activate }), { signal });
      const version = String(response.version);
      setNotice(response.problems.length ? { tone: 'warning', text: t('savedProblems', { version }) } : { tone: 'success', text: t(response.activated ? 'savedActive' : 'saved', { version }) });
      setComment('');
      if (name !== existing) onOpen(name); else onReload(response.version);
    } catch (error) { setNotice({ tone: 'danger', text: t('saveFailed', { message: error instanceof Error ? error.message : String(error) }) }); throw error; }
  });

  const html = current?.kind === 'html' ? preview.value?.parts[current.name] : undefined;
  return <Stack gap={16}><ConnectionNotice />
    <PageHeader title={<span style={mono}>{existing || name || t('untitled')}</span>} description={description || t('description')}
      meta={<Stack direction="row" gap={8}>{template?.activeVersion ? <StatusBadge tone="success">{t('active')} v{String(template.activeVersion)}</StatusBadge> : existing ? <StatusBadge tone="warning">{t('notActivated')}</StatusBadge> : null}
        {loaded && loaded.version !== template?.activeVersion && <Badge variant="outline">{t('version')} {String(loaded.version)}</Badge>}{dirty && <Badge variant="outline">{t('dirty')}</Badge>}</Stack>}
      actions={<Stack direction="row" gap={8}><Button variant="ghost" size="sm" onClick={onBack}>{t('back')}</Button>
        {existing && <ConfirmAction trigger={<Button variant="outline" size="sm">{t('delete')}</Button>} title={t('deleteTitle', { name: existing })} description={t('deleteHelp')} confirmLabel={t('delete')}
          onConfirm={async (signal) => { await client.deleteTemplate(create(DeleteTemplateRequestSchema, { name: existing }), { signal }); onBack(); }} />}</Stack>} />
    {notice && <p role={notice.tone === 'danger' ? 'alert' : 'status'} style={{ margin: 0 }}><StatusBadge tone={notice.tone}>{notice.text}</StatusBadge></p>}
    <div style={{ display: 'grid', gap: 16, gridTemplateColumns: 'repeat(auto-fit, minmax(min(100%, 520px), 1fr))', alignItems: 'start' }}>
      <Stack gap={16}>
        <Panel title={t('templates')}>
          <Stack gap={12}>
            <Field><FieldLabel htmlFor="template-name">{t('name')}</FieldLabel>
              <Input id="template-name" value={name} disabled={!!existing} aria-invalid={!!name && !nameValid} style={mono} onChange={(event) => setName(event.target.value.trim())} />
              <FieldDescription>{t('nameHelp')}</FieldDescription></Field>
            <Field><FieldLabel htmlFor="template-description">{t('descriptionLabel')}</FieldLabel>
              <Input id="template-description" value={description} onChange={(event) => setDescription(event.target.value)} /></Field>
          </Stack>
        </Panel>
        <Panel title={t('parts')} actions={<Stack direction="row" gap={4} style={{ alignItems: 'center' }}>
          <Input aria-label={t('partName')} placeholder={t('partName')} value={newPart} style={{ ...mono, width: 140 }} onChange={(event) => setNewPart(event.target.value.trim())} />
          <Button size="sm" variant="outline" disabled={!partPattern.test(newPart) || parts.some((p) => p.name === newPart)}
            onClick={() => { setParts([...parts, create(PartSchema, { name: newPart, kind: 'text', source: '' })]); setSelected(newPart); setNewPart(''); }}>{t('addPart')}</Button></Stack>}>
          <Stack gap={12}>
            <div role="tablist" aria-label={t('parts')} style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>{parts.map((p) => {
              const broken = problems.some((problem) => problem.part === p.name);
              return <Button key={p.name} role="tab" aria-selected={p.name === current?.name} size="sm" variant={p.name === current?.name ? 'default' : 'outline'} onClick={() => setSelected(p.name)}>
                <span style={mono}>{p.name}</span><Badge variant="outline">{p.kind}</Badge>{broken && <StatusBadge tone="danger" dot={false}>!</StatusBadge>}</Button>;
            })}</div>
            {current && <>
              <Stack direction="row" gap={8} style={{ alignItems: 'center' }}>
                <SelectControl aria-label={t('kind')} value={current.kind} onValueChange={(kind) => update(current.name, { kind })} options={[{ value: 'text', label: t('text') }, { value: 'html', label: t('html') }]} />
                <span style={muted}>{t('kindHelp')}</span>
                <span style={{ flex: 1 }} />
                <Button size="sm" variant="ghost" disabled={parts.length < 2} onClick={() => { const rest = parts.filter((p) => p.name !== current.name); setParts(rest); setSelected(rest[0]?.name ?? ''); }}>{t('removePart', { name: current.name })}</Button>
              </Stack>
              <CodeEditor key={current.name} label={t('source', { name: current.name })} value={current.source} onChange={(source) => update(current.name, { source })} language="text" mode={mode}
                height={current.name === 'subject' || current.name === 'title' ? 96 : 320} complete={complete(sample)} diagnostics={diagnostics} onSubmit={() => { if (canSave) doSave(); }} />
              <p style={{ ...muted, margin: 0 }}><b>{t('functions')}:</b> {t('functionsHelp')}</p>
            </>}
          </Stack>
        </Panel>
        <Panel title={t('sample')} description={t('sampleHelp')}>
          <CodeEditor label={t('sample')} value={sample} onChange={setSample} language="json" mode={mode} height={200} />
        </Panel>
        <Panel title={t('save')} footer={<Stack direction="row" gap={8} style={{ alignItems: 'center' }}>
          <Button disabled={!canSave} onClick={doSave}>{save.pending ? t('saving') : t('save')}</Button>
          <Stack direction="row" gap={6} style={{ alignItems: 'center' }}><Checkbox id="activate" checked={activate} onCheckedChange={(value) => setActivate(value === true)} /><Label htmlFor="activate">{t('activate')}</Label></Stack>
          {existing && !parts.length && <span role="alert">{t('error')}</span>}
          {!existing && name && nameValid && <span style={muted}>{t('nameHelp').split(';')[0]}</span>}</Stack>}>
          <Field><FieldLabel htmlFor="template-comment">{t('comment')}</FieldLabel>
            <Input id="template-comment" value={comment} onChange={(event) => setComment(event.target.value)} /><FieldDescription>{t('commentHelp')}</FieldDescription></Field>
        </Panel>
      </Stack>
      <Stack gap={16}>
        <Panel title={t('preview')} actions={preview.loading ? <span style={muted}>{t('previewing')}</span> : undefined}>
          <Stack gap={12}>
            {problems.length ? <div role="alert"><b>{t('problems')}</b><ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>{problems.map((p, i) => <li key={i} style={{ fontSize: 13 }}><span style={mono}>{p.part}</span>: {p.message}</li>)}</ul></div>
              : preview.value ? <StatusBadge tone="success">{t('noProblems')}</StatusBadge> : null}
            {!preview.value ? <span style={muted}>{t('previewEmpty')}</span> : parts.map((p) => <div key={p.name}>
              <div style={{ ...muted, marginBottom: 4 }}><span style={mono}>{p.name}</span> · {p.kind}</div>
              {p.kind === 'html' && preview.value?.parts[p.name] !== undefined
                ? <iframe title={`${t('preview')} ${p.name}`} sandbox="" srcDoc={preview.value.parts[p.name]} style={{ width: '100%', height: p.name === current?.name && html ? 360 : 200, border: '1px solid var(--border)', borderRadius: 6, background: 'white' }} />
                : <pre style={{ ...mono, margin: 0, padding: 8, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', border: '1px solid var(--border)', borderRadius: 6, minHeight: 32 }}>{preview.value?.parts[p.name] ?? '—'}</pre>}
            </div>)}
          </Stack>
        </Panel>
        {template && <Versions template={template} parts={parts} mode={mode} client={client} diffOf={diffOf} onDiff={setDiffOf} onLoad={(version) => onReload(version)}
          onActivated={(version) => { setNotice({ tone: 'success', text: t('activated', { version: String(version) }) }); onReload(loaded?.version); }} />}
      </Stack>
    </div>
  </Stack>;
}

function Versions({ template, parts, mode, client, diffOf, onDiff, onLoad, onActivated }: {
  template: GetTemplateResponse; parts: Part[]; mode: 'dark' | 'light'; client: Client; diffOf?: bigint; onDiff: (version?: bigint) => void; onLoad: (version: bigint) => void; onActivated: (version: bigint) => void;
}) {
  const t = useText();
  const other = usePlatformQuery(`templates:diff:${template.name}:${diffOf ?? ''}`, (signal) => diffOf
    ? client.getTemplate(create(GetTemplateRequestSchema, { name: template.name, version: diffOf }), { signal }) : Promise.resolve(undefined));
  return <Panel title={t('versions')} count={template.versions.length} flush>
    <Table><TableHeader><TableRow><TableHead>{t('version')}</TableHead><TableHead>{t('comment')}</TableHead><TableHead>{t('created')}</TableHead><TableHead /></TableRow></TableHeader>
      <TableBody>{template.versions.map((v) => <TableRow key={String(v.version)}>
        <TableCell style={mono}>v{String(v.version)}{v.version === template.activeVersion && <StatusBadge tone="success" style={{ marginLeft: 6 }}>{t('active')}</StatusBadge>}
          {v.version === template.content?.version && <Badge variant="outline" style={{ marginLeft: 6 }}>{t('edit')}</Badge>}</TableCell>
        <TableCell>{v.comment || <span style={muted}>—</span>}</TableCell>
        <TableCell><Time value={date(v.createdAt)} /></TableCell>
        <TableCell><Stack direction="row" gap={4}>
          <Button size="sm" variant={diffOf === v.version ? 'default' : 'ghost'} onClick={() => onDiff(diffOf === v.version ? undefined : v.version)}>{t('diff')}</Button>
          {v.version !== template.content?.version && <Button size="sm" variant="ghost" onClick={() => onLoad(v.version)}>{t('loadVersion')}</Button>}
          {v.version !== template.activeVersion && <ConfirmAction trigger={<Button size="sm" variant="outline">{t('activateVersion')}</Button>} title={t('activateTitle', { version: String(v.version) })} description={t('activateHelp')} confirmLabel={t('activateVersion')}
            onConfirm={async (signal) => { await client.activateVersion(create(ActivateVersionRequestSchema, { name: template.name, version: v.version }), { signal }); onActivated(v.version); }} />}
        </Stack></TableCell>
      </TableRow>)}</TableBody></Table>
    {diffOf && other.value?.content && <Stack gap={12} style={{ padding: 12 }}>{[...new Set([...other.value.content.parts.map((p) => p.name), ...parts.map((p) => p.name)])].map((part) => <div key={part}>
      <div style={{ ...muted, marginBottom: 4 }}>{t('diffOf', { part, version: String(diffOf) })}</div>
      <DiffViewer label={t('diffOf', { part, version: String(diffOf) })} before={other.value!.content!.parts.find((p) => p.name === part)?.source ?? ''} after={parts.find((p) => p.name === part)?.source ?? ''} mode={mode} language="text" height={160} />
    </div>)}</Stack>}
  </Panel>;
}

function Missing() {
  const t = useText(), navigate = usePluginNavigate();
  return <Panel><EmptyState title={<h1 style={{ margin: 0, fontSize: 16, fontWeight: 500 }}>{t('missing')}</h1>} action={<Button size="sm" variant="outline" onClick={() => navigate('')}>{t('back')}</Button>} /></Panel>;
}
export const definition = definePlugin({ ...navigation, routes: [
  { index: true, element: <List /> }, { path: 'new', element: <Editor /> }, { path: 'edit/:name', element: <Editor /> }, { path: '*', element: <Missing /> },
] });
