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
  ActivateVersionRequestSchema, DeleteTemplateRequestSchema, GetSettingsRequestSchema, GetTemplateRequestSchema, ListTemplatesRequestSchema, PartSchema, PreviewRequestSchema,
  SaveTemplateRequestSchema, TranslationSchema, type GetTemplateResponse, type Problem,
} from './gen/proto/templates/console/v1/admin_pb';
import navigation from './navigation';

const date = (value?: Timestamp) => value ? timestampDate(value) : undefined;
const mono = { fontFamily: 'var(--font-mono)' } as const;
const muted = { color: 'var(--muted-foreground)', fontSize: 12 } as const;
const namePattern = /^_?[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$/;
const partPattern = /^[a-z][a-z0-9_]*$/;
type PartDef = { name: string; kind: string };
type Sources = Record<string, Record<string, string>>;
type Notice = { tone: 'success' | 'warning' | 'danger'; text: string };
const starter: PartDef[] = [{ name: 'subject', kind: 'text' }, { name: 'text', kind: 'text' }, { name: 'html', kind: 'html' }];
function useText() { return useTranslation('module.templates').t; }
const route = (name: string) => `edit/${encodeURIComponent(name)}`;
/** JSON text in one canonical form, for comparing drafts; invalid text as is. */
function canonical(text: string) { try { return JSON.stringify(JSON.parse(text || '{}')); } catch { return text; } }
/** The canonical BCP 47 form of a tag, or undefined. */
function languageTag(tag: string) { try { return Intl.getCanonicalLocales(tag.trim())[0]; } catch { return undefined; } }
function languageName(tag: string, display: string) {
  try { return new Intl.DisplayNames([display], { type: 'language' }).of(tag) ?? tag; } catch { return tag; }
}

type Client = ReturnType<typeof useClient<InstanceType<typeof AdminServiceClient>>>;
function useSettings(client: Client) {
  return usePlatformQuery('templates:settings', (signal) => client.getSettings(create(GetSettingsRequestSchema), { signal }));
}

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
        : <Table><TableHeader><TableRow><TableHead>{t('name')}</TableHead><TableHead>{t('descriptionLabel')}</TableHead><TableHead>{t('parts')}</TableHead><TableHead>{t('languages')}</TableHead><TableHead>{t('active')}</TableHead><TableHead>{t('versions')}</TableHead><TableHead>{t('updated')}</TableHead></TableRow></TableHeader>
          <TableBody>{state.value.templates.map((tpl) => <TableRow key={tpl.name} tabIndex={0} style={{ cursor: 'pointer' }} onClick={() => navigate(route(tpl.name))}
            onKeyDown={(event) => { if (event.key === 'Enter') navigate(route(tpl.name)); }}>
            <TableCell style={mono}>{tpl.name}</TableCell>
            <TableCell>{tpl.description || <span style={muted}>—</span>}</TableCell>
            <TableCell><Stack direction="row" gap={4}>{tpl.parts.map((part) => <Badge key={part} variant="outline">{part}</Badge>)}</Stack></TableCell>
            <TableCell><Stack direction="row" gap={4}>{tpl.languages.map((lang) => tpl.incomplete.includes(lang)
              ? <StatusBadge key={lang} tone="warning" title={t('incomplete', { languages: lang })}>{lang}</StatusBadge>
              : <Badge key={lang} variant={lang === tpl.defaultLanguage ? 'default' : 'outline'}>{lang}</Badge>)}</Stack></TableCell>
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
const helpers = ['plural', 'number', 'longdate', 'monthname', 'weekday', 'date', 'upper', 'lower', 'title', 'trim', 'replace', 'join', 'default', 'json', 'add', 'mul', 'round', 'range', 'if', 'with', 'else', 'end', 'template'];
function complete(sample: string) {
  return async ({ value, position }: { value: string; position: number }): Promise<EditorCompletionResult | null> => {
    const before = value.slice(0, position), open = before.lastIndexOf('{{');
    if (open < 0 || before.lastIndexOf('}}') > open) return null;
    const inside = before.slice(open + 2);
    const field = /(?:^|[\s(|])\$?\.([\w.]*)$/.exec(inside);
    if (field) {
      const segments = field[1].split('.'), word = segments.pop() ?? '';
      return { from: position - word.length, options: sampleKeys(sample, segments).map((key) => ({ label: key, type: 'property' as const })) };
    }
    const word = /(?:^|[\s(|-])(\w*)$/.exec(inside)?.[1] ?? '';
    return { from: position - word.length, options: helpers.filter((h) => h.startsWith(word)).map((label) => ({ label, type: 'function' as const })) };
  };
}

/** Debounced value: the preview runs after typing settles. */
function useSettled<T>(value: T, ms = 400) {
  const [settled, setSettled] = useState(value);
  useEffect(() => { const id = setTimeout(() => setSettled(value), ms); return () => clearTimeout(id); }, [value, ms]);
  return settled;
}

/** The editor: parts, languages, sample, live preview, versions. */
function Editor() {
  const t = useText(), navigate = usePluginNavigate(), client = useClient(AdminServiceClient), { mode } = usePluginContext();
  const params = useParams(), existing = params.name ? decodeURIComponent(params.name) : '';
  const [version, setVersion] = useState(0n), [notice, setNotice] = useState<Notice>();
  const settings = useSettings(client);
  const state = usePlatformQuery(`templates:get:${existing}:${version}`, (signal) => existing
    ? client.getTemplate(create(GetTemplateRequestSchema, { name: existing, version }), { signal }) : Promise.resolve(undefined), { keep: true });
  if ((existing && state.error !== undefined) || settings.error !== undefined) return <EmptyState title={t('error')} role="alert" action={<Button size="sm" variant="outline" onClick={() => navigate('')}>{t('back')}</Button>} />;
  if ((existing && !state.value) || !settings.value) return <Stack gap={8} role="status" aria-label={t('loading')}><Skeleton style={{ height: 40 }} /><Skeleton style={{ height: 320 }} /></Stack>;
  return <Draft key={`${existing}:${state.value?.content?.version ?? 0n}`} existing={existing} template={state.value} mode={mode} notice={notice} onNotice={setNotice}
    offered={settings.value.languages} installationDefault={settings.value.defaultLanguage}
    onReload={(next) => { setVersion(next ?? 0n); state.refresh(); }} onOpen={(name) => navigate(route(name))} onBack={() => navigate('')} client={client} />;
}

function initialSources(template: GetTemplateResponse | undefined, lang: string): Sources {
  const content = template?.content;
  if (!content?.translations.length) return { [lang]: Object.fromEntries(starter.map((p) => [p.name, ''])) };
  return Object.fromEntries(content.translations.map((tr) => [tr.language, { ...tr.sources }]));
}

function Draft({ existing, template, mode, notice, onNotice: setNotice, offered, installationDefault, onReload, onOpen, onBack, client }: {
  existing: string; template?: GetTemplateResponse; mode: 'dark' | 'light'; notice?: Notice; onNotice: (notice?: Notice) => void;
  offered: string[]; installationDefault: string;
  onReload: (version?: bigint) => void; onOpen: (name: string) => void; onBack: () => void; client: Client;
}) {
  const t = useText(), { i18n } = useTranslation();
  const loaded = template?.content;
  const [name, setName] = useState(existing), [description, setDescription] = useState(template?.description ?? '');
  const [parts, setParts] = useState<PartDef[]>(() => loaded?.parts.length ? loaded.parts.map((p) => ({ name: p.name, kind: p.kind })) : starter);
  const [defaultLanguage, setDefaultLanguage] = useState(loaded?.defaultLanguage || installationDefault);
  const [sources, setSources] = useState<Sources>(() => initialSources(template, loaded?.defaultLanguage || installationDefault));
  const [sample, setSample] = useState(() => loaded?.sample && loaded.sample !== '{}' ? JSON.stringify(JSON.parse(loaded.sample), null, 2) : '{\n  \n}');
  const languages = useMemo(() => [defaultLanguage, ...Object.keys(sources).filter((l) => l !== defaultLanguage).sort()], [sources, defaultLanguage]);
  const [language, setLanguage] = useState(defaultLanguage), [selected, setSelected] = useState(parts[0]?.name ?? '');
  const [comment, setComment] = useState(''), [activate, setActivate] = useState(!existing);
  const [newPart, setNewPart] = useState(''), [newLanguage, setNewLanguage] = useState('');
  const [diffOf, setDiffOf] = useState<bigint>();
  const save = usePlatformAction<void>();

  const snapshot = (d: string, p: PartDef[], s: Sources, def: string, smp: string) =>
    JSON.stringify({ d, p: p.map((x) => [x.name, x.kind]), s: Object.keys(s).sort().map((l) => [l, p.map((x) => s[l]?.[x.name] ?? '')]), def, smp: canonical(smp) });
  const baseline = useMemo(() => snapshot(template?.description ?? '', loaded?.parts.length ? loaded.parts.map((p) => ({ name: p.name, kind: p.kind })) : starter,
    initialSources(template, loaded?.defaultLanguage || installationDefault), loaded?.defaultLanguage || installationDefault, loaded?.sample ?? ''), [template, loaded, installationDefault]);
  const touched = snapshot(description, parts, sources, defaultLanguage, sample) !== baseline;
  const dirty = touched && (!!existing || !!name || Object.values(sources).some((s) => Object.values(s).some(Boolean)));
  const current = parts.find((p) => p.name === selected) ?? parts[0];
  const lang = languages.includes(language) ? language : defaultLanguage;
  const sampleValid = useMemo(() => { try { const v = JSON.parse(sample || '{}'); return !!v && typeof v === 'object' && !Array.isArray(v); } catch { return false; } }, [sample]);
  const nameValid = namePattern.test(name);
  const untranslated = (l: string, part: string) => {
    const src = sources[l]?.[part] ?? '';
    return !src.trim() || (l !== defaultLanguage && src === (sources[defaultLanguage]?.[part] ?? ''));
  };

  // Live preview of the language on screen, rendered by the service's own engine with its rules.
  const settled = useSettled(JSON.stringify({ parts, lang, sources: sources[lang] ?? {}, sample }));
  const preview = usePlatformQuery(`templates:preview:${settled}`, (signal) => {
    const draft = JSON.parse(settled) as { parts: PartDef[]; lang: string; sources: Record<string, string>; sample: string };
    let data = draft.sample; try { JSON.parse(data || '{}'); } catch { data = '{}'; }
    return client.preview(create(PreviewRequestSchema, { parts: draft.parts.map((p) => create(PartSchema, p)),
      translation: create(TranslationSchema, { language: draft.lang, sources: draft.sources }), data }), { signal });
  }, { keep: true });
  const problems: Problem[] = preview.value?.problems ?? [];
  const source = current ? sources[lang]?.[current.name] ?? '' : '';
  const diagnostics: EditorDiagnostic[] = problems.filter((p) => p.part === current?.name).map((p) => {
    const line = /:(\d+):/.exec(p.message)?.[1];
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

  const setSource = (part: string, text: string) => setSources((old) => ({ ...old, [lang]: { ...old[lang], [part]: text } }));
  const addPart = () => {
    setParts([...parts, { name: newPart, kind: 'text' }]);
    setSources((old) => Object.fromEntries(Object.entries(old).map(([l, s]) => [l, { ...s, [newPart]: '' }])));
    setSelected(newPart); setNewPart('');
  };
  const removePart = (part: string) => {
    const rest = parts.filter((p) => p.name !== part);
    setParts(rest); setSelected(rest[0]?.name ?? '');
    setSources((old) => Object.fromEntries(Object.entries(old).map(([l, s]) => { const { [part]: _, ...kept } = s; return [l, kept]; })));
  };
  const tag = languageTag(newLanguage);
  const addLanguage = () => {
    if (!tag || sources[tag]) return;
    setSources((old) => ({ ...old, [tag]: { ...old[lang] } })); setLanguage(tag); setNewLanguage('');
  };
  const removeLanguage = (l: string) => {
    setSources((old) => { const { [l]: _, ...kept } = old; return kept; }); setLanguage(defaultLanguage);
  };
  const canSave = nameValid && sampleValid && parts.length > 0 && !save.pending && !save.disabled && (dirty || !existing);
  const doSave = () => void save.run(async (signal) => {
    setNotice(undefined);
    try {
      const response = await client.saveTemplate(create(SaveTemplateRequestSchema, {
        name, description, parts: parts.map((p) => create(PartSchema, p)), defaultLanguage, sample, comment, activate,
        translations: languages.map((l) => create(TranslationSchema, { language: l, sources: Object.fromEntries(parts.map((p) => [p.name, sources[l]?.[p.name] ?? ''])) })),
      }), { signal });
      const version = String(response.version);
      const issues = response.problems.map((p) => `${p.language}/${p.part}: ${p.message}`).join('; ');
      setNotice(response.problems.length ? { tone: 'warning', text: `${t('savedProblems', { version })} ${issues}` } : { tone: 'success', text: t(response.activated ? 'savedActive' : 'saved', { version }) });
      setComment('');
      if (name !== existing) onOpen(name); else onReload(response.version);
    } catch (error) { setNotice({ tone: 'danger', text: t('saveFailed', { message: error instanceof Error ? error.message : String(error) }) }); throw error; }
  });

  const addable = offered.filter((l) => !sources[l]);
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
        <Panel title={t('languages')} description={t('languageHelp')} actions={<Stack direction="row" gap={4} style={{ alignItems: 'center' }}>
          {addable.length > 0 && <SelectControl aria-label={t('addLanguage')} value="" onValueChange={(l) => { setSources((old) => ({ ...old, [l]: { ...old[lang] } })); setLanguage(l); }}
            options={[{ value: '', label: t('addLanguage'), disabled: true }, ...addable.map((l) => ({ value: l, label: `${l} · ${languageName(l, i18n.language)}` }))]} />}
          <Input aria-label={t('languageTag')} placeholder={t('languageTag')} value={newLanguage} aria-invalid={!!newLanguage && !tag} style={{ ...mono, width: 150 }} onChange={(event) => setNewLanguage(event.target.value)}
            onKeyDown={(event) => { if (event.key === 'Enter') addLanguage(); }} />
          <Button size="sm" variant="outline" disabled={!tag || !!sources[tag]} onClick={addLanguage}>{t('addLanguage')}</Button></Stack>}>
          <Stack gap={8}>
            <div role="tablist" aria-label={t('languages')} style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>{languages.map((l) => {
              const missing = parts.some((p) => untranslated(l, p.name));
              return <Button key={l} role="tab" aria-selected={l === lang} size="sm" variant={l === lang ? 'default' : 'outline'} onClick={() => setLanguage(l)} title={languageName(l, i18n.language)}>
                <span style={mono}>{l}</span>{l === defaultLanguage && <Badge variant="outline">{t('defaultLanguage')}</Badge>}{missing && <StatusBadge tone="warning" dot={false}>!</StatusBadge>}</Button>;
            })}</div>
            <Stack direction="row" gap={8} style={{ alignItems: 'center' }}>
              <span style={muted}>{languageName(lang, i18n.language)}</span><span style={{ flex: 1 }} />
              {lang !== defaultLanguage && <Button size="sm" variant="ghost" onClick={() => setDefaultLanguage(lang)}>{t('makeDefault')}</Button>}
              {lang !== defaultLanguage && <Button size="sm" variant="ghost" onClick={() => removeLanguage(lang)}>{t('removeLanguage', { language: lang })}</Button>}
            </Stack>
          </Stack>
        </Panel>
        <Panel title={t('parts')} actions={<Stack direction="row" gap={4} style={{ alignItems: 'center' }}>
          <Input aria-label={t('partName')} placeholder={t('partName')} value={newPart} style={{ ...mono, width: 140 }} onChange={(event) => setNewPart(event.target.value.trim())} />
          <Button size="sm" variant="outline" disabled={!partPattern.test(newPart) || parts.some((p) => p.name === newPart)} onClick={addPart}>{t('addPart')}</Button></Stack>}>
          <Stack gap={12}>
            <div role="tablist" aria-label={t('parts')} style={{ display: 'flex', gap: 4, flexWrap: 'wrap' }}>{parts.map((p) => {
              const broken = problems.some((problem) => problem.part === p.name);
              return <Button key={p.name} role="tab" aria-selected={p.name === current?.name} size="sm" variant={p.name === current?.name ? 'default' : 'outline'} onClick={() => setSelected(p.name)}>
                <span style={mono}>{p.name}</span><Badge variant="outline">{p.kind}</Badge>{broken ? <StatusBadge tone="danger" dot={false}>!</StatusBadge> : untranslated(lang, p.name) && <StatusBadge tone="warning" dot={false}>?</StatusBadge>}</Button>;
            })}</div>
            {current && <>
              <Stack direction="row" gap={8} style={{ alignItems: 'center' }}>
                <SelectControl aria-label={t('kind')} value={current.kind} onValueChange={(kind) => setParts(parts.map((p) => p.name === current.name ? { ...p, kind } : p))} options={[{ value: 'text', label: t('text') }, { value: 'html', label: t('html') }]} />
                <span style={muted}>{t('kindHelp')}</span>
                <span style={{ flex: 1 }} />
                <Button size="sm" variant="ghost" disabled={parts.length < 2} onClick={() => removePart(current.name)}>{t('removePart', { name: current.name })}</Button>
              </Stack>
              {untranslated(lang, current.name) && lang !== defaultLanguage && <span role="status" style={{ ...muted, color: 'var(--warning, #b45309)' }}>{t('untranslated', { language: defaultLanguage })}</span>}
              <CodeEditor key={`${lang}:${current.name}`} label={t('source', { name: `${lang}/${current.name}` })} value={source} onChange={(text) => setSource(current.name, text)} language="text" mode={mode}
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
          <Stack direction="row" gap={6} style={{ alignItems: 'center' }}><Checkbox id="activate" checked={activate} onCheckedChange={(value) => setActivate(value === true)} /><Label htmlFor="activate">{t('activate')}</Label></Stack></Stack>}>
          <Field><FieldLabel htmlFor="template-comment">{t('comment')}</FieldLabel>
            <Input id="template-comment" value={comment} onChange={(event) => setComment(event.target.value)} /><FieldDescription>{t('commentHelp')}</FieldDescription></Field>
        </Panel>
      </Stack>
      <Stack gap={16}>
        <Panel title={t('previewIn', { language: lang })} actions={preview.loading ? <span style={muted}>{t('previewing')}</span> : undefined}>
          <Stack gap={12}>
            {problems.length ? <div role="alert"><b>{t('problems')}</b><ul style={{ margin: '4px 0 0', paddingLeft: 18 }}>{problems.map((p, i) => <li key={i} style={{ fontSize: 13 }}><span style={mono}>{p.part}</span>: {p.message}</li>)}</ul></div>
              : preview.value ? <StatusBadge tone="success">{t('noProblems')}</StatusBadge> : null}
            {!preview.value ? <span style={muted}>{t('previewEmpty')}</span> : parts.map((p) => <div key={p.name}>
              <div style={{ ...muted, marginBottom: 4 }}><span style={mono}>{p.name}</span> · {p.kind}</div>
              {p.kind === 'html' && preview.value?.parts[p.name] !== undefined
                ? <iframe title={`${t('preview')} ${p.name}`} sandbox="" srcDoc={preview.value.parts[p.name]} style={{ width: '100%', height: 240, border: '1px solid var(--border)', borderRadius: 6, background: 'white' }} />
                : <pre style={{ ...mono, margin: 0, padding: 8, whiteSpace: 'pre-wrap', overflowWrap: 'anywhere', border: '1px solid var(--border)', borderRadius: 6, minHeight: 32 }}>{preview.value?.parts[p.name] ?? '—'}</pre>}
            </div>)}
          </Stack>
        </Panel>
        {template && <Versions template={template} language={lang} parts={parts} sources={sources[lang] ?? {}} mode={mode} client={client} diffOf={diffOf} onDiff={setDiffOf} onLoad={(version) => onReload(version)}
          onActivated={(version) => { setNotice({ tone: 'success', text: t('activated', { version: String(version) }) }); onReload(loaded?.version); }} />}
      </Stack>
    </div>
  </Stack>;
}

function Versions({ template, language, parts, sources, mode, client, diffOf, onDiff, onLoad, onActivated }: {
  template: GetTemplateResponse; language: string; parts: PartDef[]; sources: Record<string, string>; mode: 'dark' | 'light'; client: Client;
  diffOf?: bigint; onDiff: (version?: bigint) => void; onLoad: (version: bigint) => void; onActivated: (version: bigint) => void;
}) {
  const t = useText();
  const other = usePlatformQuery(`templates:diff:${template.name}:${diffOf ?? ''}`, (signal) => diffOf
    ? client.getTemplate(create(GetTemplateRequestSchema, { name: template.name, version: diffOf }), { signal }) : Promise.resolve(undefined));
  const before = other.value?.content?.translations.find((tr) => tr.language === language)?.sources ?? {};
  const names = [...new Set([...(other.value?.content?.parts.map((p) => p.name) ?? []), ...parts.map((p) => p.name)])];
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
    {diffOf && other.value?.content && <Stack gap={12} style={{ padding: 12 }}>{names.map((part) => <div key={part}>
      <div style={{ ...muted, marginBottom: 4 }}>{t('diffOf', { part: `${language}/${part}`, version: String(diffOf) })}</div>
      <DiffViewer label={t('diffOf', { part: `${language}/${part}`, version: String(diffOf) })} before={before[part] ?? ''} after={sources[part] ?? ''} mode={mode} language="text" height={160} />
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
