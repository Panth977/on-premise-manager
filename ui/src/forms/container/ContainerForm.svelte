<script lang="ts" module>
  import type { SiteVolumeKind } from '@opm/shared/types';
  /** "+ New data volume…" of a mount row: the app opens ITS volume drawer, then calls `done(volumeSlug)` once declared. */
  export interface NewVolumeRequest {
    suggested: string;
    kind: SiteVolumeKind;
    /** e.g. "for api · data" */
    context: string;
    done: (volumeSlug: string) => void;
  }
</script>

<script lang="ts">
  /**
   * The container form (SPEC §21 #52/#53, §21 #102) — ONE form for the cloud container page (create / edit / duplicate /
   * read) and the agent container editor (local edit; read when there is no working copy). Same sections, fields,
   * labels, pickers and validation everywhere:
   *   1 Service, version and name · 2 Ports · 3 Volumes · 4 Environment · 5 Runtime · 6 Resolved
   *   aside: On this page · Summary · Validation (one-click fixes).
   * What differs is only what the ConfigHost locks (agent: service / version / name are "Cloud only") and what the app
   * passes in (new-volume drawer, image-page links, file preview, extra actions). Edits `value` in place.
   */
  import type { Snippet } from 'svelte';
  import type { Config, Container, ContainerEnv, ContainerMount, ContainerPort, ImageEnv, Site, SiteVolume, ValidationIssue, VolumeSource } from '@opm/shared/types';
  import type { ExprContext, Resolved, ResolveResult } from '@opm/shared/expr';
  import { isExpr } from '@opm/shared/expr';
  import {
    RE,
    RESERVED_CONTAINER_SLUGS,
    containerExprContext,
    containerMounts,
    containerPorts,
    defaultVolumeSlug,
    imagePortNumber,
    imageVolumeKind,
    locationLabel,
    resolveContainerConfig,
    suggestContainerSlug,
    suggestVolumeSlug,
    varValues,
    volumeUsers,
    VOLUME_KIND_LABEL,
  } from '@opm/shared/validate';
  import { inferDepends, startOrder } from '@opm/shared/depends';
  import FormPage from '../../primitives/FormPage.svelte';
  import FormSection from '../../primitives/FormSection.svelte';
  import Field from '../../primitives/Field.svelte';
  import FieldIssues from '../../primitives/FieldIssues.svelte';
  import Issues from '../../primitives/Issues.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Select from '../../primitives/Select.svelte';
  import MultiSelect from '../../primitives/MultiSelect.svelte';
  import Combobox from '../../primitives/Combobox.svelte';
  import ChipList from '../../primitives/ChipList.svelte';
  import ExprInput from '../../primitives/ExprInput.svelte';
  import ValueText from '../../primitives/ValueText.svelte';
  import InfoTip from '../../primitives/InfoTip.svelte';
  import SharedBadge from '../../primitives/SharedBadge.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import type { SelectOption } from '../../primitives/listbox';
  import HealthCheckForm from './HealthCheckForm.svelte';
  import { useConfigHost, issuesUnder, type FormMode } from '../host';
  import { LOCK_FIXED } from '../locks';
  import { secretOptions } from '../pickers';
  import { applyVersion, envDefault, freshContainer, imageEnvList, mergeEnv, parseEnvBlock, setEnvKind, sourceOf, sourceValue } from './container';

  interface Props {
    /** the container (edited in place); null in create mode until a service is picked */
    value: Container | null;
    /** the site working copy the container belongs to (volumes, other containers, routes) */
    site: Site;
    mode: FormMode;
    /** issues of the last refused save (cloud API `invalid`) shown with the live ones */
    extraIssues?: readonly ValidationIssue[];
    /** create from a duplicate: the source container (note under the name) */
    duplicateOf?: string | null;
    /** agent home of the site (server paths in hints); undefined = "<agentHome>" */
    agentHome?: string;
    onnewvolume?: (req: NewVolumeRequest) => void;
    /** "Declare it" of a mount that picks an undeclared volume */
    ondeclare?: (volumeSlug: string) => void;
    /** §21 #101 V_FILE_IN_WRITABLE_VOLUME one-click fix (cloud, saved containers only) */
    onmoveintocontent?: () => void;
    /** template file mounted here: open its rendered preview (agent) */
    onpreviewfile?: (fileSlug: string) => void;
    /** the image page of one image volume (cloud fix link for V_VOLUME_KIND_*) */
    imageVolumeHref?: (name: string) => string;
    /** create: the name changed (cloud renames the `<old>-<imageVolume>` volumes it declared on this page) */
    onnamechange?: (old: string, next: string) => void;
    /** app links */
    href?: { volumes?: string; settings?: string; containers?: string; container?: (slug: string) => string };
    /** extra content of the aside (e.g. the cloud's Save / Cancel) and under the sections (Duplicate / Remove) */
    aside?: Snippet;
    footer?: Snippet;
    /** live counts for the app chrome (badges, Save state) */
    errors?: number;
    warnings?: number;
    nameError?: string | null;
  }
  let {
    value = $bindable(null),
    site,
    mode,
    extraIssues = [],
    duplicateOf = null,
    agentHome,
    onnewvolume,
    ondeclare,
    onmoveintocontent,
    onpreviewfile,
    imageVolumeHref,
    onnamechange,
    href = {},
    aside: asideExtra,
    footer,
    errors = $bindable(0),
    warnings = $bindable(0),
    nameError = $bindable(null),
  }: Props = $props();

  const host = useConfigHost();
  const ro = $derived(mode === 'read' || host.readOnly);
  const isNew = $derived(mode === 'create');
  const siteSlug = $derived(site.siteSlug);
  const c = $derived(value);
  const others = $derived(site.config.containers.filter((x) => x !== value && x.containerSlug !== (isNew ? '\u0000' : value?.containerSlug)));
  const taken = $derived(others.map((x) => x.containerSlug));
  const secretSlugs = $derived(host.config().secrets.map((s) => s.secretSlug));

  // ───── image
  const svc = $derived(c ? host.service(c.serviceSlug) : undefined);
  const img = $derived(c ? host.image(c.serviceSlug, c.version) : undefined);
  const imgEnv = $derived<ImageEnv[]>(imageEnvList(img));
  const ie = (name: string): ImageEnv | null => imgEnv.find((x) => x.name === name) ?? null;

  // ───── validation: the working config (a new container is validated as if it were on the site)
  const cfg = $derived.by<Config>(() => {
    const w = host.config();
    if (!isNew || !value) return w;
    const snap = $state.snapshot(value) as Container;
    return { ...w, sites: w.sites.map((s) => (s.siteSlug === siteSlug ? { ...s, config: { ...s.config, containers: [...s.config.containers.filter((x) => x.containerSlug !== snap.containerSlug || x !== value), snap] } } : s)) };
  });
  const all = $derived(host.validate(cfg));
  const base = $derived(c ? `sites[${siteSlug}].config.containers[${c.containerSlug}]` : '');
  const nameErr = $derived.by<string | null>(() => {
    if (!c || !isNew) return null;
    const n = c.containerSlug;
    if (!n) return 'Required';
    if (!RE.slug.test(n)) return '2–40 characters: a–z, 0–9 and "-", starting with a letter';
    if ((RESERVED_CONTAINER_SLUGS as readonly string[]).includes(n)) return `"${n}" is reserved`;
    if (taken.includes(n)) return `a container named ${n} already exists on site ${siteSlug}`;
    return null;
  });
  const mine = $derived.by<ValidationIssue[]>(() => {
    if (!c) return [];
    const list = [...issuesUnder(all, base), ...issuesUnder(extraIssues, base).filter((s) => !all.some((a) => a.path === s.path && a.code === s.code))];
    return isNew && nameErr ? [{ path: `${base}.containerSlug`, code: 'V_NAME', message: nameErr, severity: 'error' as const }, ...list.filter((x) => x.path !== `${base}.containerSlug`)] : list;
  });
  $effect(() => {
    errors = mine.filter((i) => i.severity === 'error').length;
    warnings = mine.filter((i) => i.severity === 'warning').length;
    nameError = nameErr;
  });
  const at = (p: string) => mine.filter((x) => x.path === `${base}.${p}`);
  const under = (p: string) => mine.filter((x) => x.path === p || x.path.startsWith(p + '.') || x.path.startsWith(p + '['));
  const sev = (xs: readonly ValidationIssue[]) => xs.some((x) => x.severity === 'error');
  const lockOf = (field: string) => (c ? host.lock(`${base}.${field}`) : null);

  // ───── resolution (shared resolveContainerConfig: env + mount paths in dependency order, container.env.<NAME>, E_CYCLE)
  const vctx = $derived(host.vctx?.() ?? null);
  const snap = $derived(c ? ($state.snapshot(c) as Container) : null);
  const baseCtx = $derived.by<ExprContext | null>(() => {
    if (!snap || !vctx) return null;
    try {
      return containerExprContext(cfg, vctx, siteSlug, snap);
    } catch {
      return null;
    }
  });
  const res = $derived.by(() => {
    if (!snap || !baseCtx || !img || !vctx) return null;
    try {
      return resolveContainerConfig(cfg, vctx, snap, { env: imgEnv, volumes: img.volumes }, baseCtx);
    } catch {
      return null;
    }
  });
  /** ctx for the inputs: container.env.<NAME> = the resolved values of THIS container (completion + previews) */
  const ectx = $derived.by<ExprContext | null>(() => {
    if (!baseCtx) return null;
    const env: Record<string, { value: string | null; secret: boolean }> = {};
    for (const s of res?.env ?? []) {
      const r = res!.resolution.env[s.name];
      const secret = !!s.secretRef || (r?.ok ? r.result.secret : false);
      env[s.name] = { value: r?.ok && (s.src !== null || s.secretRef) ? (secret ? r.result.masked : r.result.value) : null, secret };
    }
    for (const en of snap?.env ?? []) if (!(en.name in env) && en.name) env[en.name] = { value: null, secret: en.secretRef !== null };
    return { ...baseCtx, container: { slug: snap?.containerSlug ?? '', env } };
  });
  const envNames = $derived([...new Set([...imgEnv.map((e) => e.name), ...(c?.env.map((e) => e.name) ?? [])])].filter((n) => RE.envName.test(n)));
  const envRes = (name: string): Resolved<ResolveResult> | null => res?.resolution.env[name] ?? null;
  const mountRes = (key: string): Resolved<ResolveResult> | null => res?.resolution.mounts[key] ?? null;
  const vars = $derived(varValues(cfg));

  // ───── 1 service · version · name
  const services = $derived(host.services?.() ?? []);
  const readyVersions = (slug: string) => (host.versions?.(slug) ?? []).filter((v) => !v.status || v.status === 'ready');
  const svcOpts = $derived<SelectOption[]>(
    services.map((s) => {
      const vs = host.versions?.(s.serviceSlug) ?? [];
      return { value: s.serviceSlug, label: s.serviceSlug, hint: [s.label !== s.serviceSlug ? s.label : '', vs.length ? `${vs.length} version${vs.length === 1 ? '' : 's'}` : 'no versions'].filter(Boolean).join(' · '), disabled: !readyVersions(s.serviceSlug).length };
    }),
  );
  const verOpts = $derived<SelectOption[]>(
    c
      ? [
          ...(host.versions?.(c.serviceSlug) ?? []).map((v) => ({ value: v.version, label: v.version, hint: v.reference, badge: v.status ? { text: v.kind === 'job' ? `${v.status} · job` : v.status, tone: v.status === 'ready' ? ('green' as const) : ('amber' as const) } : v.kind === 'job' ? { text: 'job', tone: 'blue' as const } : undefined, disabled: !!v.status && v.status !== 'ready' && v.version !== c.version })),
          ...(!(host.versions?.(c.serviceSlug) ?? []).some((v) => v.version === c.version) ? [{ value: c.version, label: c.version, hint: 'not in the catalog', badge: { text: 'missing', tone: 'red' as const } }] : []),
        ]
      : [],
  );
  let nameEdited = $state(false);
  function pickService(slug: string) {
    if (c && c.serviceSlug === slug) return;
    const ver = readyVersions(slug)[0]?.version ?? host.versions?.(slug)[0]?.version ?? '';
    const im = host.image(slug, ver);
    if (!im) return;
    const name = nameEdited && c ? c.containerSlug : suggestContainerSlug(slug, taken);
    value = freshContainer(slug, ver, im, name, secretSlugs);
  }
  function setName(n: string) {
    if (!c) return;
    const old = c.containerSlug;
    nameEdited = true;
    c.containerSlug = n;
    onnamechange?.(old, n);
  }
  function setVersion(v: string) {
    if (!c || v === c.version) return;
    applyVersion(c, v, host.image(c.serviceSlug, v));
  }
  const sameImage = $derived(c ? others.filter((x) => x.serviceSlug === c.serviceSlug && x.version === c.version).map((x) => x.containerSlug) : []);

  // ───── 2 ports
  const portRows = $derived(c && img ? containerPorts(c, img) : []);
  const portNum = (name: string) => {
    const ip = img?.ports.find((x) => x.name === name);
    return ip ? imagePortNumber(ip, vars, host.deploymentId) : null;
  };
  function portRow(name: string): ContainerPort {
    let r = c!.ports.find((x) => x.portName === name);
    if (!r) {
      c!.ports.push({ portName: name, hostPort: null, hostIp: '0.0.0.0', edge: true });
      r = c!.ports[c!.ports.length - 1]!;
    }
    return r;
  }
  let exprPorts = $state<Record<string, boolean>>({});
  const portIsExpr = (name: string, hp: ContainerPort['hostPort']) => typeof hp === 'string' || !!exprPorts[name];
  /** a free host port: the container port when free and not an edge / agent port, else the next free one from 8080 */
  function freePort(name: string): number {
    const used = new Set<number>();
    for (const x of site.config.containers) for (const p of x.ports) if (typeof p.hostPort === 'number') used.add(p.hostPort);
    [site.config.agentPort, site.config.agentGuiAliasPort, site.config.edge.httpPort, site.config.edge.httpsPort, 80, 443].forEach((p) => p && used.add(p));
    const n = portNum(name);
    if (n !== null && n > 0 && !used.has(n)) return n;
    let p = 8080;
    while (used.has(p)) p++;
    return p;
  }
  function setPublished(name: string, on: boolean) {
    portRow(name).hostPort = on ? freePort(name) : null;
    if (!on) exprPorts = { ...exprPorts, [name]: false };
  }
  function togglePortExpr(name: string) {
    const r = portRow(name);
    const toExpr = !portIsExpr(name, r.hostPort);
    exprPorts = { ...exprPorts, [name]: toExpr };
    if (!toExpr && typeof r.hostPort === 'string') {
      const n = Number(r.hostPort);
      r.hostPort = Number.isInteger(n) && n > 0 ? n : freePort(name);
    }
  }
  const BIND = [
    { value: '0.0.0.0' as const, label: 'all (0.0.0.0)', title: 'Every address of the machine (LAN + localhost)' },
    { value: '127.0.0.1' as const, label: 'local (127.0.0.1)', title: 'Only this machine (localhost)' },
  ];
  const routesTo = (portName: string) => site.config.edge.routes.filter((r) => r.action.type === 'proxy' && r.action.containerSlug === c?.containerSlug && r.action.portName === portName).map((r) => r.routeId);

  // ───── 3 volumes (a mount picks a DECLARED site volume, a file, a secret, a host path or "not mounted")
  const MOUNT_PH = '/app/helper/config/env/{{ container.env.NODE_ENV }}.ts';
  const MOUNT_HELP =
    'Mount paths may use values of this container: /app/helper/config/env/{{ container.env.NODE_ENV }}.ts mounts at the file named after the final NODE_ENV (the image default or the value set here). Roots: container.env.<NAME>, var.*, self.*, deployment.id. Must start with "/".';
  const RENAME_TIP =
    "Recommended for Node-RED: a FOLDER data volume mounted at /data with its own content (flows.json, settings.js — Site › Volumes › the volume › Content), so Node-RED can save by rename; turn on Persist to cloud there to keep the site's edits in the cloud.";
  const KIND_FIX = new Set(['V_VOLUME_KIND_IMAGE', 'V_VOLUME_KIND_MISMATCH']);
  const siteVols = $derived<SiteVolume[]>(site.config.volumes ?? []);
  const users = $derived(volumeUsers(cfg.sites.find((x) => x.siteSlug === siteSlug)?.config ?? { containers: [] }));
  const sharedWith = (slug: string) => [...new Set((users[slug] ?? []).map((u) => u.containerSlug).filter((x) => x !== c?.containerSlug))];
  function copyUsers(src: VolumeSource): string[] {
    if (src.kind !== 'file' && src.kind !== 'secret') return [];
    const out = new Set<string>();
    for (const x of cfg.sites.find((s) => s.siteSlug === siteSlug)?.config.containers ?? [])
      if (x.containerSlug !== c?.containerSlug)
        for (const v of x.volumes ?? []) if (v.source.kind === src.kind && (src.kind === 'file' ? (v.source as { fileSlug: string }).fileSlug === src.fileSlug : (v.source as { secretSlug: string }).secretSlug === src.secretSlug)) out.add(x.containerSlug);
    return [...out];
  }
  /** where the one copy of a source lives on the server */
  function serverOf(src: VolumeSource): string | null {
    if (src.kind === 'volume') return locationLabel(siteVols.find((x) => x.volumeSlug === src.volumeSlug)?.location, 'volumes', src.volumeSlug, agentHome);
    if (src.kind === 'file') return locationLabel(host.config().files.find((f) => f.fileSlug === src.fileSlug)?.location, 'files', src.fileSlug, agentHome);
    if (src.kind === 'secret') return `${agentHome ?? '<agentHome>'}/secrets/${src.secretSlug} (0400)`;
    if (src.kind === 'host') return src.path || null;
    return null;
  }
  const isTemplate = (fileSlug: string) => {
    const f = host.config().files.find((x) => x.fileSlug === fileSlug);
    return !!f && f.editor === 'text' && (f.render ?? 'expressions') === 'expressions';
  };
  function sourceOptions(current: string | null, imageRow: boolean): SelectOption[] {
    const out: SelectOption[] = [];
    for (const v of siteVols) {
      const sw = sharedWith(v.volumeSlug);
      out.push({ value: `volume:${v.volumeSlug}`, label: v.volumeSlug, group: 'Data volumes', icon: v.kind === 'file' ? 'file' : 'archive', hint: sw.length ? `shared with ${sw.join(', ')}` : v.label && v.label !== v.volumeSlug ? v.label : undefined, badge: v.kind === 'file' ? { text: v.backup ? 'single file' : 'single file · no backup', tone: v.backup ? 'gray' : 'amber' } : v.backup ? undefined : { text: 'no backup', tone: 'amber' } });
    }
    if (current?.startsWith('volume:') && !siteVols.some((v) => `volume:${v.volumeSlug}` === current)) out.push({ value: current, label: `${current.slice(7)} (not declared)`, icon: 'alert', group: 'Data volumes' });
    if (!ro && onnewvolume) out.push({ value: 'new-volume', label: '+ New data volume…', group: 'Data volumes', action: true });
    for (const f of host.config().files) out.push({ value: `file:${f.fileSlug}`, label: f.fileSlug, hint: f.label && f.label !== f.fileSlug ? f.label : undefined, icon: 'file', group: 'Files' });
    for (const s of secretOptions(host)) out.push({ value: `secret:${s.value}`, label: s.value, hint: s.hint, icon: 'key', group: 'Secrets' });
    out.push({ value: 'host', label: 'Host path…', hint: 'a folder of the machine (never backed up)', icon: 'server', group: 'Advanced' });
    if (imageRow) out.push({ value: 'none', label: 'Not mounted', hint: 'container storage only: lost when the container is recreated', icon: 'x', group: 'Advanced' });
    return out;
  }
  type RowRef = { image: string } | { extra: number };
  function volRow(name: string): ContainerMount {
    let r = c!.volumes.find((v) => v.imageVolume === name);
    if (!r) {
      c!.volumes.push({ imageVolume: name, mountPath: null, source: { kind: 'none' }, readOnly: false });
      r = c!.volumes[c!.volumes.length - 1]!;
    }
    return r;
  }
  const rowOf = (r: RowRef): ContainerMount | null => ('image' in r ? volRow(r.image) : (c?.volumes[r.extra] ?? null));
  function setSource(row: ContainerMount, src: VolumeSource) {
    const prev = row.source.kind;
    row.source = src;
    // file / secret are always read-only; volume / host default to read-write (a change of kind into them resets it)
    if (src.kind === 'file' || src.kind === 'secret') row.readOnly = true;
    else if (src.kind === 'none' || (prev !== 'volume' && prev !== 'host')) row.readOnly = false;
  }
  function pickSource(r: RowRef, v: string, kind?: SiteVolumeKind) {
    if (v === 'new-volume') {
      if (!onnewvolume || !c) return;
      const iv = 'image' in r ? img?.volumes.find((x) => x.name === r.image) : undefined;
      onnewvolume({
        suggested: suggestVolumeSlug('image' in r ? defaultVolumeSlug(c.containerSlug || 'container', r.image) : `${c.containerSlug || 'container'}-data`, siteVols.map((x) => x.volumeSlug)),
        kind: kind ?? (iv ? imageVolumeKind(iv) : 'dir'),
        context: 'image' in r ? `for ${c.containerSlug || 'this container'} · ${r.image}` : `for ${c.containerSlug || 'this container'} · extra mount`,
        done: (slug) => {
          const row = rowOf(r);
          if (row) setSource(row, { kind: 'volume', volumeSlug: slug });
        },
      });
      return;
    }
    const row = rowOf(r);
    if (row) setSource(row, sourceOf(v, row.source));
  }
  let editPath = $state<Record<string, boolean>>({});
  function resetPath(name: string) {
    const row = c?.volumes.find((v) => v.imageVolume === name);
    if (row) row.mountPath = null;
    editPath = { ...editPath, [name]: false };
  }
  function addExtra() {
    c?.volumes.push({ imageVolume: null, mountPath: '', source: { kind: 'none' }, readOnly: false });
  }
  const isRw = (k: VolumeSource['kind'] | undefined) => k === 'volume' || k === 'host';
  const extraRows = $derived((c?.volumes ?? []).map((v, i) => ({ v, i })).filter((x) => x.v.imageVolume === null));
  const relative = (p: string | null) => typeof p === 'string' && p !== '' && !p.startsWith('/');

  // ───── 4 env
  const firstSecret = () => secretSlugs[0] ?? '';
  const envIdx = (name: string) => c?.env.findIndex((x) => x.name === name) ?? -1;
  const secOpts = $derived(secretOptions(host));
  function override(name: string) {
    const d = ie(name);
    if (!c || !d) return;
    c.env.push(d.secret ? { name, value: null, secretRef: firstSecret() } : { name, value: envDefault(d), secretRef: null });
  }
  function resetEnv(name: string) {
    if (!c) return;
    c.env = c.env.filter((x) => x.name !== name);
    exprRows = { ...exprRows, [name]: false };
  }
  function addEnv() {
    c?.env.push({ name: '', value: '', secretRef: null });
  }
  let exprRows = $state<Record<string, boolean>>({});
  const useChoice = (en: ContainerEnv, key: string) => {
    const d = ie(en.name);
    return !!d?.options && en.secretRef === null && !exprRows[key] && !(en.value && isExpr(en.value));
  };
  function toggleExpr(en: ContainerEnv, key: string) {
    const toExpr = useChoice(en, key);
    exprRows = { ...exprRows, [key]: toExpr };
    if (!toExpr) {
      const d = ie(en.name);
      if (d?.options && (!en.value || !d.options.includes(en.value))) en.value = envDefault(d);
    }
  }
  const choiceOptions = (d: ImageEnv, cur: string | null): SelectOption[] => [
    ...(cur !== null && !(d.options ?? []).includes(cur) ? [{ value: cur, label: `${cur || '(empty)'} — not allowed`, badge: { text: 'not allowed', tone: 'red' as const } }] : []),
    ...(d.options ?? []).map((o) => ({ value: o, label: o, badge: o === d.value ? { text: 'default', tone: 'gray' as const } : undefined })),
  ];
  const extraEnv = $derived((c?.env ?? []).map((en, i) => ({ en, i })).filter((x) => !ie(x.en.name)));
  const extraNameOptions = $derived(imgEnv.filter((x) => !c?.env.some((y) => y.name === x.name)).map((x) => ({ value: x.name, hint: x.description || 'image env' })));
  function nameExtra(i: number, name: string) {
    const en = c!.env[i]!;
    en.name = name.trim().toUpperCase();
    const d = ie(en.name);
    if (d) {
      if (d.secret) ((en.value = null), (en.secretRef = en.secretRef ?? firstSecret()));
      else if (en.secretRef === null && (!en.value || (d.options && !d.options.includes(en.value)))) en.value = envDefault(d);
    }
  }
  let pasted = $state<string | null>(null);
  function onEnvPaste(e: ClipboardEvent, i: number) {
    const text = e.clipboardData?.getData('text') ?? '';
    const rows = parseEnvBlock(text);
    if (!rows || !c || ro) return;
    e.preventDefault();
    // the row being typed in is replaced by the pasted block
    if (!c.env[i]?.name && !c.env[i]?.value) c.env.splice(i, 1);
    const n = mergeEnv(c, rows);
    pasted = `${n} env ${n === 1 ? 'value' : 'values'} pasted`;
    setTimeout(() => (pasted = null), 4000);
  }
  const envSummary = (x: ImageEnv) => [x.required ? 'required' : '', x.secret ? 'secret' : '', x.options ? `one of ${x.options.length}` : '', x.value !== undefined ? `default ${x.value === '' ? '""' : x.value}` : ''].filter(Boolean).join(' · ') || 'optional';

  // ───── 5 runtime
  const RESTARTS = $derived([
    { value: 'always' as const, label: 'always', title: img?.kind === 'job' ? 'Not for jobs' : 'Restart whenever it stops', disabled: img?.kind === 'job' },
    { value: 'on-failure' as const, label: 'on-failure', title: 'Restart after a non-zero exit' },
    { value: 'never' as const, label: 'never', title: 'Run once' },
  ]);
  const depOpts = $derived<SelectOption[]>(others.map((x) => ({ value: x.containerSlug, label: x.containerSlug, hint: `${x.serviceSlug}:${x.version}${x.enabled ? '' : ' · disabled'}` })));
  // §21 #93: implicit depends (containers.<slug>.* expressions) and suggested ones (literal hostname mentions → one-click Add)
  const siteDeps = $derived.by(() => {
    const s = cfg.sites.find((x) => x.siteSlug === siteSlug);
    if (!s || !vctx) return null;
    try {
      const deps = inferDepends({ site: s.config, images: vctx.images, files: cfg.files, fileContents: host.fileContents?.() });
      return { deps, order: startOrder(s.config, deps) };
    } catch {
      return null;
    }
  });
  const inferred = $derived(c ? (siteDeps?.deps.find((d) => d.containerSlug === c.containerSlug) ?? null) : null);
  function addDepend(target: string) {
    if (!c || ro || c.depends.some((d) => d.containerSlug === target)) return;
    c.depends = [...c.depends, { containerSlug: target }];
  }
  const suggestionTarget = (i: ValidationIssue) => {
    if (i.code !== 'V_DEPENDS_SUGGESTED' || i.path !== `${base}.depends`) return null;
    return /`([^`]+)`/.exec(i.message)?.[1] ?? null;
  };
  const round2 = (n: number) => Math.round(n * 100) / 100;
  function setHcOverride(on: boolean) {
    if (!c) return;
    if (!on) c.healthcheck = null;
    else c.healthcheck = img?.healthcheck ? structuredClone($state.snapshot(img.healthcheck)) : img?.ports.length ? { type: 'tcp', portName: img.ports[0]!.name, intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 30 } : { type: 'exec', command: [], expectExitCode: 0, intervalSec: 10, timeoutSec: 5, retries: 3, startPeriodSec: 30 };
  }
  const hcText = (h: Container['healthcheck']) => {
    if (!h) return 'none';
    if (h.type === 'http') return `http ${h.portName} ${h.path} → ${h.expectStatus}`;
    if (h.type === 'tcp') return `tcp ${h.portName}`;
    if (h.type === 'exec') return `exec ${h.command.join(' ')}`;
    return `log /${h.pattern}/`;
  };

  // ───── summary + resolved
  const published = $derived(portRows.filter((p) => p.hostPort !== null));
  const hostPortText = (portName: string, hp: ContainerPort['hostPort']) => {
    if (typeof hp === 'number') return String(hp);
    const n = baseCtx?.site?.[siteSlug]?.port?.[c?.containerSlug ?? '']?.[portName];
    return n !== null && n !== undefined ? `${n} (${hp})` : String(hp);
  };
  const mountsSummary = $derived.by(() => {
    if (!c || !img) return [];
    return containerMounts(c, img).map((m) => {
      const r = mountRes(m.key);
      const src = m.source.kind === 'volume' ? `volume ${m.source.volumeSlug}` : m.source.kind === 'host' ? `host ${m.source.path || '?'}` : m.source.kind === 'file' ? `file ${m.source.fileSlug || '?'}` : m.source.kind === 'secret' ? `secret ${m.source.secretSlug || '?'}` : '';
      return { key: m.key, src, server: m.source.kind === 'host' ? null : serverOf(m.source), path: r?.ok ? r.result.value : m.mountPath, error: r && !r.ok ? `${r.error.code}: ${r.error.message}` : null, ro: m.readOnly };
    });
  });
  const preview = $derived.by(() => {
    if (!res) return [];
    return res.env
      .map((s) => {
        const r = res.resolution.env[s.name];
        const source = s.secretRef ? `secret ${c?.env.find((x) => x.name === s.name)?.secretRef ?? ''}` : s.field === 'imageEnv' ? 'image default' : s.src !== null && isExpr(s.src) ? 'expression' : 'value';
        if (s.field === 'imageEnv' && s.src === null) return { name: s.name, source: 'image (no default)', text: null as string | null, error: null as string | null, secret: false };
        return r?.ok ? { name: s.name, source, text: r.result.masked, error: null, secret: r.result.secret || !!s.secretRef } : { name: s.name, source, text: null, error: r ? `${r.error.code}: ${r.error.message}` : null, secret: false };
      })
      .sort((a, b) => a.name.localeCompare(b.name));
  });
  const SECTION_OF: Record<string, string> = { ports: 'sec-ports', volumes: 'sec-volumes', env: 'sec-env', containerSlug: 'sec-service', version: 'sec-service', serviceSlug: 'sec-service' };
  function jumpTo(i: ValidationIssue) {
    const m = /\.(ports|volumes|env|healthcheck|depends|command|entrypoint|resources|restart|containerSlug|serviceSlug|version)/.exec(i.path.slice(base.length));
    const id = m ? (SECTION_OF[m[1]!] ?? 'sec-runtime') : null;
    if (id) document.getElementById(id)?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }
  const count = (prefix: string) => {
    const l = under(prefix);
    const e = l.filter((x) => x.severity === 'error').length;
    const w = l.filter((x) => x.severity === 'warning').length;
    return { badge: e || w || null, tone: e ? ('red' as const) : w ? ('amber' as const) : null };
  };
  const sections = $derived(
    !c || !img
      ? [{ id: 'sec-service', label: '1 Service, version and name' }]
      : [
          { id: 'sec-service', label: '1 Service, version and name', ...count(`${base}.containerSlug`) },
          { id: 'sec-ports', label: '2 Ports', ...count(`${base}.ports`) },
          { id: 'sec-volumes', label: '3 Volumes', ...count(`${base}.volumes`) },
          { id: 'sec-env', label: '4 Environment', ...count(`${base}.env`) },
          { id: 'sec-runtime', label: '5 Runtime' },
          { id: 'sec-resolved', label: '6 Resolved' },
        ],
  );
</script>

{#snippet usersOf(list: string[], testid: string, title: string)}
  <div><SharedBadge items={list} current={c?.containerSlug ?? null} {title} href={href.container} {testid} /></div>
{/snippet}

{#snippet lockedText(text: string, lock: string | null, testid: string, icon?: boolean)}
  <div class="input mono locked" aria-readonly="true" title={lock ?? undefined} data-testid={testid}>
    {#if icon}<Icon name="box" size={14} />{/if}<span class="lt">{text}</span>
  </div>
{/snippet}

{#snippet srcExtras(row: ContainerMount | null, key: string, onhost: (p: string) => void, what: string)}
  {#if row?.source.kind === 'none'}
    <span class="hint">container storage only — lost when the container is recreated</span>
  {:else if row?.source.kind === 'host'}
    <input class="input sm mono" value={row.source.path} oninput={(ev) => onhost(ev.currentTarget.value)} placeholder="/srv/core/data" disabled={ro} aria-label="Host path of {what}" data-testid="{key}-host" />
    <span class="hint">never backed up</span>
  {:else if row?.source.kind === 'volume'}
    {@const slug = row.source.volumeSlug}
    {@const sw = sharedWith(slug)}
    {@const sv = siteVols.find((x) => x.volumeSlug === slug)}
    {#if !sv}
      <div class="field-err row wrap gap-s">volume {slug} is not declared on this site{#if !ro && ondeclare}<button type="button" class="lnk" onclick={() => ondeclare(slug)} data-testid="{key}-declare">Declare it</button>{/if}</div>
    {:else}
      <span class="hint" data-testid="{key}-server">server: <span class="mono">{serverOf(row.source)}</span>{sv.backup ? ' · backed up' : ' · not backed up'}</span>
      {#if sw.length}{@render usersOf(sw, `${key}-shared`, 'Containers of this site that mount this volume')}{/if}
    {/if}
  {:else if row && (row.source.kind === 'file' || row.source.kind === 'secret')}
    {@const cu = copyUsers(row.source)}
    <span class="hint" data-testid="{key}-server">server: <span class="mono">{serverOf(row.source)}</span></span>
    {#if cu.length}{@render usersOf(cu, `${key}-shared-copy`, 'One copy on the site, mounted by')}{/if}
    {#if row.source.kind === 'file' && onpreviewfile && isTemplate(row.source.fileSlug)}{@const fs = row.source.fileSlug}<Btn size="xs" variant="ghost" icon="eye" onclick={() => onpreviewfile(fs)} data-testid="{key}-preview">Preview</Btn>{/if}
  {/if}
{/snippet}

{#snippet roCell(row: ContainerMount | null, key: string)}
  {#if row && isRw(row.source.kind)}
    <Toggle checked={row.readOnly} onchange={(on) => (row.readOnly = on)} disabled={ro} label={row.readOnly ? 'read-only' : 'read-write'} ariaLabel="Read-only" testid="{key}-ro" />
  {:else if row && (row.source.kind === 'file' || row.source.kind === 'secret')}
    <span class="badge outline" title="file and secret mounts are always read-only" data-testid="{key}-ro-badge"><Icon name="lock" size={10} />read-only</span>
  {:else}<span class="hint">—</span>{/if}
{/snippet}

{#snippet issueLines(errs: ValidationIssue[], key: string, ivName: string | null, rel: boolean)}
  {#each errs.filter((x) => !(rel && (x.code === 'V_PATH' || x.code === 'V_MOUNT'))) as x, k (k)}
    <div class={x.severity === 'error' ? 'field-err' : x.severity === 'info' ? 'field-info' : 'field-warn'} data-testid="{key}-issue" data-code={x.code}>
      <span class="issue-code">{x.code}</span>{x.code === 'V_VOLUME_NOT_MOUNTED' ? 'Not mounted — pick a data volume (or choose “Not mounted”): its data is lost when the container is recreated.' : x.message}
      {#if ivName && KIND_FIX.has(x.code)}
        {@const want = x.code === 'V_VOLUME_KIND_MISMATCH' ? 'file' : 'dir'}
        <div class="fixes" data-testid="{key}-fixes">
          {#if !ro && onnewvolume}<button type="button" class="lnk" onclick={() => pickSource({ image: ivName }, 'new-volume', want)} data-testid="{key}-fix-volume">+ New {want === 'file' ? 'single-file' : 'folder'} volume…</button>{/if}
          {#if imageVolumeHref}<a class="lnk" href={imageVolumeHref(ivName)} data-testid="{key}-fix-image">Set image volume {ivName} to {VOLUME_KIND_LABEL[want === 'file' ? 'dir' : 'file']} on the image page <Icon name="external" size={11} /></a>{:else}<span class="hint">or change image volume {ivName} on the cloud image page</span>{/if}
        </div>
      {:else if x.code === 'V_FILE_VOLUME_RENAME'}
        <span class="tipq">Recommended for Node-RED <InfoTip text={RENAME_TIP} label="Recommended for Node-RED" size={12} testid="{key}-rename-tip" /></span>
      {:else if x.code === 'V_FILE_IN_WRITABLE_VOLUME' && !ro && onmoveintocontent}
        <div class="fixes"><button type="button" class="lnk" onclick={onmoveintocontent} data-testid="{key}-move-into">Move into the volume's content</button></div>
      {/if}
    </div>
  {/each}
{/snippet}

{#snippet envControl(en: ContainerEnv, key: string, errs: ValidationIssue[])}
  {@const d = ie(en.name)}
  {@const choice = useChoice(en, key)}
  <div class="ctl">
    <SegmentedControl size="sm" options={[{ value: 'value' as const, label: 'value' }, { value: 'secret' as const, label: 'secret' }]} value={en.secretRef !== null ? 'secret' : 'value'} onchange={(k) => setEnvKind(en, k, d, firstSecret())} disabled={ro} ariaLabel="Value or secret of {en.name}" testid="env-kind-{key}" />
    <div class="grow">
      {#if en.secretRef !== null}
        <Combobox value={en.secretRef} options={secOpts} mono size="sm" placeholder="choose a secret" emptyText="No secrets defined" disabled={ro} invalid={sev(errs)} ariaLabel="Secret of {en.name}" testid="env-secret-{key}" onchange={(v) => (en.secretRef = v)} />
      {:else}
        <div class="vrow">
          {#if choice && d}
            <div class="grow"><Select value={en.value ?? ''} options={choiceOptions(d, en.value)} mono size="sm" disabled={ro} invalid={sev(errs)} ariaLabel="Value of {en.name}" testid="env-choice-{key}" onchange={(v) => (en.value = v)} /></div>
          {:else}
            <div class="grow"><ExprInput value={en.value ?? ''} onchange={(v) => (en.value = v)} field="env" ctx={ectx} {envNames} resolved={envRes(en.name)} size="sm" disabled={ro} invalid={sev(errs)} placeholder={d?.value !== undefined ? `image default: ${d.value}` : 'value or {{ expression }}'} testid="env-value-{key}" ariaLabel="Value of {en.name || 'env'}" /></div>
          {/if}
          {#if d?.options && !ro}
            <Btn size="sm" variant="ghost" icon={choice ? 'braces' : 'list'} aria-pressed={!choice} aria-label={choice ? `Use an expression for ${en.name}` : `Choose from the allowed values of ${en.name}`} title={choice ? 'Write an expression instead' : 'Back to the list of allowed values'} onclick={() => toggleExpr(en, key)} data-testid="env-expr-{key}" />
          {/if}
        </div>
      {/if}
    </div>
  </div>
  {#each errs as x, k (k)}<div class={x.severity === 'error' ? 'field-err' : x.severity === 'info' ? 'field-info' : 'field-warn'} data-testid="env-issue-{key}"><span class="issue-code">{x.code}</span>{x.message}</div>{/each}
{/snippet}

<fieldset class="form" disabled={ro} data-testid="container-form">
  <FormPage {sections} testid="container-page">
    <!-- 1 service · version · name -->
    <FormSection n={1} title="Service, version and name" id="sec-service" testid="sec-service">
      <div class="form-grid">
        <Field label="Service" id="c-svc" required group={isNew} lock={isNew ? null : (lockOf('serviceSlug') ?? LOCK_FIXED)} hint={isNew ? 'Any service; the same image may run many times on a site.' : svc?.label && svc.label !== c?.serviceSlug ? svc.label : undefined}>
          {#if isNew}
            <Select id="c-svc" value={c?.serviceSlug ?? null} options={svcOpts} mono searchable placeholder="Search services" ariaLabel="Service" testid="c-service" disabled={ro} onchange={pickService} />
          {:else if c}
            {@render lockedText(c.serviceSlug, lockOf('serviceSlug') ?? LOCK_FIXED, 'c-service-locked', true)}
          {/if}
        </Field>
        <Field label="Version" id="c-version" required group lock={c && !isNew ? lockOf('version') : null} issues={c ? at('version') : []} hint={img ? [img.reference, img.kind, img.platform].filter(Boolean).join(' · ') : c ? 'image not in the catalog' : 'Newest ready version first'}>
          {#if !c}
            <input class="input" disabled placeholder="pick a service first" />
          {:else if lockOf('version')}
            {@render lockedText(c.version, lockOf('version'), 'c-version-locked')}
          {:else}
            <Select id="c-version" value={c.version} options={verOpts} mono searchable placeholder="choose a version" ariaLabel="Version" testid="container-version" disabled={ro} invalid={!img} onchange={setVersion} />
          {/if}
        </Field>
        <Field label="Name" id="c-name" required lock={isNew ? null : (lockOf('containerSlug') ?? LOCK_FIXED)} error={c && isNew && nameErr && (c.containerSlug || nameEdited) ? nameErr : null} issues={c && !isNew ? at('containerSlug') : []} hint={isNew ? 'DNS name on the site network, used in routes, expressions and docker names. Fixed after create.' : 'DNS name on the site network · container.slug'}>
          {#if isNew}
            <input id="c-name" class="input mono" value={c?.containerSlug ?? ''} oninput={(ev) => setName(ev.currentTarget.value.trim().toLowerCase())} disabled={!c || ro} maxlength="40" placeholder="core-worker" spellcheck="false" autocomplete="off" aria-invalid={c && nameErr && (c.containerSlug || nameEdited) ? 'true' : undefined} data-testid="c-name" />
          {:else if c}
            {@render lockedText(c.containerSlug, lockOf('containerSlug') ?? LOCK_FIXED, 'c-name-locked')}
          {/if}
        </Field>
      </div>
      {#if c}
        <div class="row wrap foot">
          <Toggle checked={c.enabled} onchange={(v) => (c.enabled = v)} label={c.enabled ? 'Enabled' : 'Disabled (kept in the config, not started)'} disabled={ro} testid="container-enabled" />
          {#if sameImage.length}<SharedBadge label="same image" items={sameImage} current={c.containerSlug} title="Containers of this site that run {c.serviceSlug}:{c.version}" href={href.container} testid="same-image" />{/if}
          {#if isNew && duplicateOf}<span class="hint">Copy of <span class="mono">{duplicateOf}</span>: host ports cleared, own data volumes renamed, shared / file / secret mounts kept.</span>{/if}
        </div>
      {/if}
    </FormSection>

    {#if c && !img}
      <div class="banner error bad" data-testid="image-missing"><Icon name="alert" /><div>Image <b class="mono">{c.serviceSlug}:{c.version}</b> is not in the catalog.{isNew || !lockOf('version') ? ' Pick another version.' : ''}</div></div>
    {/if}

    {#if !c}
      <div class="card card-pad empty-pick" data-testid="pick-service"><Icon name="box" /><div><b>Pick a service</b><p class="hint">Choose the service and version this container runs. Its ports, volumes and env appear below.</p></div></div>
    {:else if img}
      <!-- 2 ports -->
      <FormSection n={2} title="Ports" id="sec-ports" testid="sec-ports" sub="HTTP should go through the edge; publish a host port only for direct access. Host ports 80 and 443 belong to the edge.">
        {#if !portRows.length}
          <p class="hint">The image exposes no ports.</p>
        {:else}
          <div class="rows" data-testid="ports-table">
            <div class="rh port-grid"><span>Port</span><span>Published</span><span>Host port</span><span>Bind address</span><span>Edge may proxy</span></div>
            {#each portRows as p (p.portName)}
              {@const errs = under(`${base}.ports[${p.portName}]`)}
              {@const routes = routesTo(p.portName)}
              <div class="r port-grid" class:err={sev(errs)} data-testid="port-row-{p.portName}">
                <div class="cell-name"><span class="mono strong">{p.portName}</span><div class="hint">{portNum(p.portName) ?? '?'}/{p.protocol}</div></div>
                <div data-label="Published"><Toggle checked={p.hostPort !== null} onchange={(on) => setPublished(p.portName, on)} disabled={ro} label={p.hostPort !== null ? 'yes' : 'no'} ariaLabel="Publish {p.portName}" testid="port-{p.portName}-published" /></div>
                <div class="c-val" data-label="Host port">
                  {#if p.hostPort === null}
                    <span class="hint">not published</span>
                  {:else}
                    <div class="vrow">
                      <div class="grow">
                        {#if portIsExpr(p.portName, p.hostPort)}
                          <ExprInput value={String(p.hostPort)} onchange={(v) => (portRow(p.portName).hostPort = /^\d+$/.test(v.trim()) ? Number(v.trim()) : v)} field="hostPort" numeric ctx={ectx} placeholder={'{{ var.port }}'} size="sm" disabled={ro} invalid={sev(errs)} testid="hostport-{p.portName}" ariaLabel="Host port of {p.portName}" />
                        {:else}
                          <NumberStepper value={typeof p.hostPort === 'number' ? p.hostPort : null} min={1} max={65535} size="sm" disabled={ro} invalid={sev(errs)} label="Host port of {p.portName}" testid="hostport-{p.portName}-num" onchange={(v) => (portRow(p.portName).hostPort = v !== null && Number.isFinite(v) ? v : null)} />
                        {/if}
                      </div>
                      {#if !ro}<Btn size="sm" variant="ghost" icon={portIsExpr(p.portName, p.hostPort) ? 'hash' : 'braces'} aria-pressed={portIsExpr(p.portName, p.hostPort)} aria-label={portIsExpr(p.portName, p.hostPort) ? `Host port of ${p.portName} as a number` : `Expression for the host port of ${p.portName}`} title={portIsExpr(p.portName, p.hostPort) ? 'Back to a number' : 'Use an expression ({{ var.… }})'} onclick={() => togglePortExpr(p.portName)} data-testid="hostport-{p.portName}-expr" />{/if}
                    </div>
                  {/if}
                  <FieldIssues issues={errs} />
                </div>
                <div data-label="Bind address"><SegmentedControl size="sm" options={BIND} value={p.hostIp} onchange={(v) => (portRow(p.portName).hostIp = v)} disabled={ro || p.hostPort === null} ariaLabel="Bind address of {p.portName}" testid="port-{p.portName}-bind" /></div>
                <div data-label="Edge may proxy">
                  <Toggle checked={p.edge} onchange={(on) => (portRow(p.portName).edge = on)} disabled={ro} label={p.edge ? 'yes' : 'no'} ariaLabel="Edge may proxy {p.portName}" testid="port-{p.portName}-edge" />
                  {#if routes.length}<div class={p.edge ? 'hint' : 'field-err'}>routes: {routes.join(', ')}</div>{/if}
                </div>
              </div>
            {/each}
          </div>
        {/if}
      </FormSection>

      <!-- 3 volumes -->
      <FormSection n={3} title="Volumes" id="sec-volumes" testid="sec-volumes" sub="Each image volume gets a data volume of the site (several containers may share one), a file, a secret or a host path.">
        {#snippet actions()}
          <InfoTip text={MOUNT_HELP} label="Help: mount path expressions" testid="mount-help" />
          {#if href.volumes}<a class="small" href={href.volumes} data-testid="site-volumes-link">Site volumes ({siteVols.length})</a>{/if}
          {#if !ro}<Btn size="sm" icon="plus" onclick={addExtra} data-testid="add-mount">Extra mount</Btn>{/if}
        {/snippet}
        {#if !img.volumes.length && !extraRows.length}
          <p class="hint">The image has no volumes. Add an extra mount for a config file, a secret, a data volume or a host path.</p>
        {/if}
        {#if img.volumes.length}
          <div class="rows" data-testid="volumes-table">
            <div class="rh vol-grid"><span>Image volume</span><span>Mounted from</span><span>Inside the container</span><span>Access</span></div>
            {#each img.volumes as iv (iv.name)}
              {@const row = c.volumes.find((v) => v.imageVolume === iv.name) ?? null}
              {@const errs = under(`${base}.volumes[${iv.name}]`)}
              {@const mr = mountRes(`image:${iv.name}`)}
              {@const mounted = !!row && row.source.kind !== 'none'}
              {@const editing = !!row && (editPath[iv.name] || row.mountPath !== null)}
              {@const rel = relative(row?.mountPath ?? null)}
              <div class="r vol-grid" class:err={sev(errs)} data-testid="vol-row-{iv.name}">
                <div class="cell-name">
                  <span class="mono strong" title="image volume {iv.name}">{iv.name}</span>
                  <span class="badge outline" title="What the image keeps at this path (set on the image page)" data-testid="vol-{iv.name}-imgkind">{VOLUME_KIND_LABEL[imageVolumeKind(iv)]}</span>
                  <div class="hint mono ip" title={iv.description ? `${iv.mountPath} — ${iv.description}` : iv.mountPath}>{iv.mountPath}</div>
                </div>
                <div class="c-src" data-label="Mounted from">
                  <Select options={sourceOptions(row ? sourceValue(row.source) : null, true)} value={row ? sourceValue(row.source) : null} placeholder="Not mounted — pick a data volume" invalid={sev(errs)} disabled={ro} ariaLabel="Source of {iv.name}" testid="vol-{iv.name}-source" size="sm" searchable onchange={(v) => pickSource({ image: iv.name }, v)} />
                  {#if !row}<span class="field-warn" data-testid="vol-{iv.name}-unpicked">Not mounted — pick a data volume</span>{/if}
                  {@render srcExtras(row, `vol-${iv.name}`, (p) => (volRow(iv.name).source = { kind: 'host', path: p }), iv.name)}
                </div>
                <div class="c-val" data-label="Inside the container">
                  {#if !mounted}
                    <span class="hint" data-testid="vol-{iv.name}-path-none">not mounted</span>
                  {:else if editing}
                    <div class="vrow">
                      <div class="grow"><ExprInput value={row?.mountPath ?? ''} onchange={(t) => (volRow(iv.name).mountPath = t.trim() === '' ? null : t)} field="mountPath" ctx={ectx} {envNames} resolved={row?.mountPath ? mr : undefined} alwaysPreview size="sm" disabled={ro} invalid={rel || sev(errs)} placeholder="image: {iv.mountPath}" ariaLabel="Mount path of {iv.name}" testid="vol-{iv.name}-mount" /></div>
                      {#if !ro}<Btn size="sm" onclick={() => resetPath(iv.name)} title="Back to the image path {iv.mountPath}" data-testid="vol-{iv.name}-reset-path">Reset</Btn>{/if}
                    </div>
                    {#if rel}<div class="field-err" data-testid="vol-{iv.name}-relative">must start with / — relative paths are invalid</div>{/if}
                  {:else}
                    <div class="pathline">
                      {#if row && (row.source.kind === 'file' || row.source.kind === 'secret')}<span class="hint" data-testid="vol-{iv.name}-ro-note">mounted read-only at</span>{/if}
                      <code class="path" data-testid="vol-{iv.name}-path">{#if mr?.ok}<ValueText text={mr.result.value} />{:else}{iv.mountPath}{/if}</code>
                      {#if !ro}<button type="button" class="lnk" onclick={() => (editPath = { ...editPath, [iv.name]: true })} data-testid="vol-{iv.name}-edit-path">Edit path</button>{/if}
                    </div>
                    {#if mr && !mr.ok}<div class="field-warn"><span class="issue-code">{mr.error.code}</span>{mr.error.message}</div>{/if}
                  {/if}
                  {@render issueLines(errs, `vol-${iv.name}`, iv.name, rel)}
                </div>
                <div data-label="Access">{@render roCell(row, `vol-${iv.name}`)}</div>
              </div>
            {/each}
          </div>
        {/if}
        {#if extraRows.length}
          <div class="rows extra" data-testid="extra-mounts">
            <div class="rh vol-grid"><span>Extra mount</span><span>Mounted from</span><span>Inside the container</span><span>Access</span></div>
            {#each extraRows as { v, i } (i)}
              {@const errs = under(`${base}.volumes[${i}]`)}
              {@const rel = relative(v.mountPath)}
              <div class="r vol-grid" class:err={rel || sev(errs)} data-testid="extra-row-{i}">
                <div class="cell-name row"><span class="hint">extra #{i + 1}</span>{#if !ro}<Btn size="sm" variant="ghost" icon="x" aria-label="Remove mount" onclick={() => c.volumes.splice(i, 1)} data-testid="extra-{i}-remove" />{/if}</div>
                <div class="c-src" data-label="Mounted from">
                  <Select options={sourceOptions(sourceValue(v.source), false)} value={sourceValue(v.source)} placeholder="Pick a source" invalid={sev(errs.filter((x) => x.path !== `${base}.volumes[${i}].mountPath`))} disabled={ro} ariaLabel="Source of the extra mount" testid="extra-{i}-source" size="sm" searchable onchange={(x) => pickSource({ extra: i }, x)} />
                  {@render srcExtras(v, `extra-${i}`, (p) => (v.source = { kind: 'host', path: p }), 'the extra mount')}
                </div>
                <div class="c-val" data-label="Inside the container">
                  {#if (v.source.kind === 'file' || v.source.kind === 'secret') && v.mountPath && !editPath[`x${i}`] && !sev(errs)}
                    {@const mr = mountRes(`volume[${i}]`)}
                    <div class="pathline"><span class="hint">mounted read-only at</span> <code class="path" data-testid="extra-{i}-path">{#if mr?.ok}<ValueText text={mr.result.value} />{:else}{v.mountPath}{/if}</code>{#if !ro}<button type="button" class="lnk" onclick={() => (editPath = { ...editPath, [`x${i}`]: true })} data-testid="extra-{i}-edit-path">Edit path</button>{/if}</div>
                  {:else}
                  <ExprInput value={v.mountPath ?? ''} onchange={(t) => (v.mountPath = t)} field="mountPath" ctx={ectx} {envNames} resolved={mountRes(`volume[${i}]`)} size="sm" disabled={ro} invalid={rel || sev(errs)} placeholder={MOUNT_PH} ariaLabel="Mount path" testid="extra-{i}-mount" />
                  {/if}
                  {#if rel}<div class="field-err" data-testid="extra-{i}-relative">must start with / — relative paths are invalid</div>{/if}
                  {@render issueLines(errs, `extra-${i}`, null, rel)}
                </div>
                <div data-label="Access">{@render roCell(v, `extra-${i}`)}</div>
              </div>
            {/each}
          </div>
        {/if}
      </FormSection>

      <!-- 4 env -->
      <FormSection n={4} title="Environment" id="sec-env" testid="sec-env" sub="Image env keeps its default until you override it; extra env adds names. Paste KEY=VALUE lines into a name to add many.">
        {#snippet actions()}
          <span class="badge">{c.env.length} set</span>
          {#if !ro}<Btn size="sm" icon="plus" onclick={addEnv} data-testid="add-env">Add env</Btn>{/if}
        {/snippet}
        {#if imgEnv.length}
          <div class="rows" data-testid="image-env">
            <div class="rh env-grid"><span>Image env</span><span>Default</span><span>This container</span><span></span></div>
            {#each imgEnv as x (x.name)}
              {@const i = envIdx(x.name)}
              {@const en = i >= 0 ? c.env[i] : null}
              {@const errs = under(`${base}.env[${x.name}]`)}
              {@const needs = !en && x.required && x.value === undefined}
              <div class="r env-grid" class:err={sev(errs)} class:over={!!en} data-testid="ienv-{x.name}">
                <div class="cell-name"><span class="mono strong">{x.name}</span><div class="hint ell" title={x.description}>{x.description || envSummary(x)}</div></div>
                <div class="c-def" data-label="Default">
                  {#if x.required}<span class="badge amber warn">required</span>{/if}
                  {#if x.secret}<span class="badge amber">secret</span>{/if}
                  {#if x.value !== undefined}<code class:struck={!!en}>{x.value === '' ? '""' : x.value}</code>{:else}<span class="hint">no default</span>{/if}
                  {#if x.options}<div class="hint">one of {x.options.join(' · ')}</div>{/if}
                </div>
                <div class="c-val" data-label="This container">
                  {#if en}
                    {@render envControl(en, x.name, errs)}
                  {:else}
                    <div class="row">
                      {#if needs}<span class="field-warn" data-testid="missing-env-{x.name}">required — set a value</span>{:else}<span class="hint">image default</span>{/if}
                      {#if !ro}<Btn size="sm" onclick={() => override(x.name)} data-testid="ienv-{x.name}-override">{needs ? 'Set' : 'Override'}</Btn>{/if}
                    </div>
                    <FieldIssues issues={errs} />
                  {/if}
                </div>
                <div class="c-x">{#if en && !ro}<Btn size="sm" variant="ghost" icon="refresh" aria-label="Back to the image default of {x.name}" title="Back to the image default" onclick={() => resetEnv(x.name)} data-testid="ienv-{x.name}-reset" />{/if}</div>
              </div>
            {/each}
          </div>
        {/if}
        {#if extraEnv.length}
          <div class="rows extra" data-testid="env-table">
            <div class="rh env-grid"><span>Extra env</span><span></span><span>Value</span><span></span></div>
            {#each extraEnv as { en, i } (i)}
              {@const key = `x${i}`}
              {@const errs = en.name ? under(`${base}.env[${en.name}]`) : []}
              <div class="r env-grid" class:err={sev(errs)} data-testid="env-row-{en.name || i}">
                <div class="cell-name span2" onpaste={(e) => onEnvPaste(e, i)} role="presentation">
                  <Combobox value={en.name} options={extraNameOptions} allowCustom mono size="sm" placeholder="NAME (or paste KEY=VALUE lines)" disabled={ro} invalid={!!en.name && !RE.envName.test(en.name)} ariaLabel="Env name" testid="env-name-{i}" onchange={(t) => nameExtra(i, t)} />
                </div>
                <div class="c-val" data-label="Value">{@render envControl(en, key, errs)}</div>
                <div class="c-x">{#if !ro}<Btn size="sm" variant="ghost" icon="x" aria-label="Remove {en.name}" onclick={() => c.env.splice(i, 1)} data-testid="env-del-{i}" />{/if}</div>
              </div>
            {/each}
          </div>
        {:else if !imgEnv.length}
          <p class="hint">The image declares no env. Add extra env with <b>Add env</b>.</p>
        {/if}
        {#if pasted}<div class="field-info" role="status" data-testid="env-pasted">{pasted}</div>{/if}
        <FieldIssues issues={at('env')} />
      </FormSection>

      <!-- 5 runtime -->
      <FormSection n={5} title="Runtime" id="sec-runtime" testid="sec-runtime">
        <div class="stack">
          <div class="form-grid">
            <Field label="Command" id="c-cmd" group hint="Empty = the image command; one argument per chip (Enter after each)" issues={at('command')}>
              <ChipList id="c-cmd" value={c.command ?? []} onchange={(l) => (c.command = l.length ? l : null)} mono placeholder={img.command ? `image: ${img.command.join(' ')}` : 'image default'} disabled={ro} testid="container-command" />
            </Field>
            <Field label="Entrypoint" id="c-ep" group hint="Empty = the image entrypoint" issues={at('entrypoint')}>
              <ChipList id="c-ep" value={c.entrypoint ?? []} onchange={(l) => (c.entrypoint = l.length ? l : null)} mono placeholder={img.entrypoint ? `image: ${img.entrypoint.join(' ')}` : 'image default'} disabled={ro} testid="container-entrypoint" />
            </Field>
          </div>
          <Field label="Restart" id="c-rs" group issues={at('restart')}>
            <SegmentedControl id="c-rs" options={RESTARTS} value={c.restart} onchange={(v) => (c.restart = v)} disabled={ro} ariaLabel="Restart policy" testid="container-restart" />
          </Field>
          <Field label="Depends on" id="c-dep" group testid="depends" hint="Containers of this site that start first and must be ready before this one starts" issues={at('depends').filter((i) => i.code !== 'V_DEPENDS_SUGGESTED')}>
            <MultiSelect id="c-dep" options={depOpts} values={c.depends.map((d) => d.containerSlug)} onchange={(v) => (c.depends = v.map((s) => ({ containerSlug: s })))} disabled={ro} placeholder={depOpts.length ? 'none' : 'No other containers on this site'} ariaLabel="Depends on" testid="container-depends" />
            <div class="hint sdeps" data-testid="site-depends-ro">
              Site {siteSlug} depends on:
              {#each site.config.depends as d (d.siteSlug)}<span class="chip">{d.siteSlug}</span>{:else}<span>no other site</span>{/each}
              {#if href.settings}<a href={href.settings} class="lnk">change <Icon name="external" size={11} /></a>{/if}
              {#if siteDeps && siteDeps.order.order.length > 1 && c.enabled}· start position {siteDeps.order.order.indexOf(c.containerSlug) + 1} of {siteDeps.order.order.length}{#if href.containers}{' '}(<a href={href.containers} class="lnk">start order</a>){/if}{/if}
            </div>
            {#if inferred?.implicit.length}
              <div class="deps-imp" data-testid="implicit-depends">
                <span class="hint">Also waits for:</span>
                {#each inferred.implicit as d (d.containerSlug)}<span class="chip mono imp" data-testid="implicit-dep-{d.containerSlug}" title="implicit (from expression): {d.from.join(', ')}">{d.containerSlug}<span class="faint"> · implicit (from expression)</span></span>{/each}
              </div>
            {/if}
            {#each inferred?.suggested ?? [] as d (d.containerSlug)}
              <div class="field-info dep-sg" data-testid="dep-suggest-{d.containerSlug}">
                <span class="issue-code">V_DEPENDS_SUGGESTED</span><span class="grow"><span class="mono">{c.containerSlug}</span> mentions <code>{d.containerSlug}</code> — add it to Depends on? <span class="faint">({d.from.join(', ')})</span></span>
                {#if !ro}<Btn size="xs" icon="plus" onclick={() => addDepend(d.containerSlug)} data-testid="dep-suggest-add-{d.containerSlug}">Add</Btn>{/if}
              </div>
            {/each}
          </Field>
          <div class="form-grid">
            <Field label="CPUs" id="c-cpu" hint="0.1–64; off = no limit" issues={at('resources.cpus')}>
              <div class="row">
                <Toggle checked={c.resources.cpus !== null} onchange={(on) => (c.resources.cpus = on ? 1 : null)} disabled={ro} label={c.resources.cpus === null ? 'no limit' : ''} ariaLabel="Limit CPUs" testid="cpus-limit" />
                {#if c.resources.cpus !== null}<NumberStepper id="c-cpu" value={c.resources.cpus} min={0.1} max={64} step={0.5} suffix="CPU" disabled={ro} label="CPUs" testid="container-cpus" onchange={(v) => (c.resources.cpus = v !== null && Number.isFinite(v) ? round2(v) : NaN)} />{/if}
              </div>
            </Field>
            <Field label="Memory" id="c-mem" hint="64–1048576 MB; off = no limit" issues={at('resources.memoryMb')}>
              <div class="row">
                <Toggle checked={c.resources.memoryMb !== null} onchange={(on) => (c.resources.memoryMb = on ? 512 : null)} disabled={ro} label={c.resources.memoryMb === null ? 'no limit' : ''} ariaLabel="Limit memory" testid="memory-limit" />
                {#if c.resources.memoryMb !== null}<NumberStepper id="c-mem" value={c.resources.memoryMb} min={64} max={1048576} step={64} suffix="MB" disabled={ro} label="Memory MB" testid="container-memory" onchange={(v) => (c.resources.memoryMb = v ?? NaN)} />{/if}
              </div>
            </Field>
          </div>
          <Field label="Health check" id="c-hc" hint={img.healthcheck ? `Image: ${hcText(img.healthcheck)}` : 'The image has no health check'} issues={c.healthcheck ? [] : at('healthcheck')}>
            <Toggle checked={c.healthcheck !== null} onchange={setHcOverride} disabled={ro} label={c.healthcheck ? 'override the image health check' : 'use the image health check'} testid="hc-override" />
          </Field>
          {#if c.healthcheck}
            <HealthCheckForm bind:value={c.healthcheck} portNames={img.ports.map((p) => p.name)} issues={mine} base="{base}.healthcheck" disabled={ro} idp="chc" />
          {/if}
        </div>
      </FormSection>

      <!-- 6 resolved -->
      <FormSection n={6} title="Resolved" id="sec-resolved" testid="expr-preview" sub="Env values and mount paths resolved in dependency order (container.env.<NAME>); secrets are masked; the site resolves again at staging.">
        {#if !preview.length && !mountsSummary.length}<p class="hint">No env values or mounts.</p>{/if}
        {#if preview.length || mountsSummary.length}
          <div class="table-wrap">
            <table class="table compact pvt" data-testid="resolved-preview">
              <thead><tr><th>Name</th><th>From</th><th>Resolved value</th></tr></thead>
              <tbody>
                {#each preview as r (r.name)}
                  <tr data-testid="pv-env-{r.name}"><td class="mono small">{r.name}</td><td class="small muted">{r.source}</td><td class="small">{#if r.error}<span class="field-warn">{r.error}</span>{:else if r.text === null}<span class="faint">unset</span>{:else}<code class:sec={r.secret}>{#if r.text === ''}""{:else}<ValueText text={r.text} />{/if}</code>{/if}</td></tr>
                {/each}
                {#each mountsSummary as m (m.key)}
                  <tr data-testid="pv-mount-{m.key}"><td class="mono small">{m.key.startsWith('image:') ? `${m.key.slice(6)} (image)` : m.src}</td><td class="small muted">mount path</td><td class="small">{#if m.error}<span class="field-warn">{m.error}</span>{:else}<code><ValueText text={m.path ?? ''} /></code>{/if}</td></tr>
                {/each}
              </tbody>
            </table>
          </div>
        {/if}
      </FormSection>

      {#if footer}{@render footer()}{/if}
    {/if}

    {#snippet aside()}
      <section class="card" data-testid="container-summary">
        <header class="card-head"><h3>Summary</h3>{#if c}{#if errors}<span class="badge red bad">{errors} {errors === 1 ? 'error' : 'errors'}</span>{/if}{#if warnings}<span class="badge amber warn">{warnings} {warnings === 1 ? 'warning' : 'warnings'}</span>{/if}{#if !errors && !warnings}<span class="badge green ok">valid</span>{/if}{/if}</header>
        <div class="card-body sum">
          <dl class="kvs">
            <dt>Name</dt><dd class="mono" data-testid="sum-name">{c?.containerSlug || '—'}</dd>
            <dt>Image</dt><dd class="mono" data-testid="sum-image">{c ? `${c.serviceSlug}:${c.version}` : '—'}</dd>
            <dt>State</dt><dd data-testid="sum-state">{c ? `${c.enabled ? 'enabled' : 'disabled'} · restart ${c.restart}` : '—'}</dd>
            <dt>Published</dt>
            <dd data-testid="sum-ports">{#each published as p (p.portName)}<div class="mono small">{p.hostIp === '127.0.0.1' ? '127.0.0.1:' : ''}{hostPortText(p.portName, p.hostPort)} → {p.portName} ({portNum(p.portName) ?? '?'}/{p.protocol})</div>{:else}<span class="faint small">no host ports</span>{/each}</dd>
            <dt>Mounts</dt>
            <dd data-testid="sum-mounts">
              {#each mountsSummary as m (m.key)}
                <div class="mnt">{#if m.error}<span class="field-warn">{m.error}</span>{:else}<code>{m.path}</code>{/if} <span class="hint">← {m.src}{m.ro ? ' · ro' : ''}{#if m.server}{' '}<span class="srv">(server: {m.server})</span>{/if}</span></div>
              {:else}<span class="faint small">none</span>{/each}
            </dd>
          </dl>
        </div>
      </section>
      {#if c}
        <div data-testid="container-issues">
          <Issues issues={mine} title="Validation" testid="container-issue-list" onpick={jumpTo}>
            {#snippet fix(i: ValidationIssue)}
              {@const t = suggestionTarget(i)}
              {#if t && !ro}<Btn size="xs" icon="plus" onclick={() => addDepend(t)} title="Add {t} to Depends on" data-testid="issue-add-depend-{t}">Add</Btn>{/if}
            {/snippet}
          </Issues>
          {#if !mine.length}<p class="hint" data-testid="container-valid">Validation: no problems.</p>{/if}
        </div>
      {/if}
      {#if asideExtra}{@render asideExtra()}{/if}
    {/snippet}
  </FormPage>
</fieldset>

<style>
  .locked {
    display: flex;
    align-items: center;
    gap: 6px;
    background: var(--surface-2);
    color: var(--text-2);
    cursor: default;
  }
  .lt {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .foot {
    margin-top: 12px;
    gap: 12px;
  }
  .row {
    display: flex;
    align-items: center;
    gap: 8px;
  }
  .row.wrap {
    flex-wrap: wrap;
  }
  .gap-s {
    gap: 6px;
  }
  .stack {
    display: flex;
    flex-direction: column;
    gap: 14px;
  }
  .grow {
    flex: 1;
    min-width: 0;
  }
  .strong {
    font-weight: var(--fw-semibold);
  }
  .empty-pick {
    display: flex;
    gap: 12px;
    align-items: flex-start;
    color: var(--text-2);
  }
  .empty-pick p {
    margin: 2px 0 0;
  }
  .rows {
    display: flex;
    flex-direction: column;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    overflow: hidden;
  }
  .rows + .rows {
    margin-top: 12px;
  }
  .rh,
  .r {
    display: grid;
    gap: 10px;
    padding: 8px 12px;
    align-items: start;
  }
  .rh {
    background: var(--surface-2);
    font-size: var(--fs-xs);
    color: var(--text-3);
    font-weight: var(--fw-medium);
    border-bottom: 1px solid var(--border);
  }
  .r + .r {
    border-top: 1px solid var(--border);
  }
  .r.err {
    background: color-mix(in srgb, var(--red-soft) 35%, transparent);
  }
  .r.over {
    background: color-mix(in srgb, var(--accent-soft) 35%, transparent);
  }
  .port-grid {
    grid-template-columns: minmax(110px, 0.8fr) 92px minmax(170px, 1.4fr) auto 110px;
  }
  .vol-grid {
    grid-template-columns: minmax(130px, 0.9fr) minmax(190px, 1.3fr) minmax(200px, 1.6fr) 120px;
  }
  .env-grid {
    grid-template-columns: minmax(130px, 0.9fr) minmax(110px, 0.8fr) minmax(220px, 2fr) 32px;
  }
  .span2 {
    grid-column: span 2;
  }
  .c-src,
  .c-val,
  .c-def,
  .cell-name {
    display: flex;
    flex-direction: column;
    gap: 4px;
    min-width: 0;
  }
  .c-def {
    flex-direction: row;
    flex-wrap: wrap;
    align-items: center;
  }
  .cell-name .ip,
  .ell {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .vrow,
  .ctl {
    display: flex;
    gap: 6px;
    align-items: flex-start;
    min-width: 0;
  }
  .pathline {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: baseline;
  }
  code.path {
    overflow-wrap: anywhere;
  }
  .struck {
    text-decoration: line-through;
    opacity: 0.6;
  }
  .lnk {
    border: 0;
    background: none;
    padding: 0;
    color: var(--accent-text);
    cursor: pointer;
    font: inherit;
    font-size: var(--fs-sm);
    text-decoration: none;
  }
  .lnk:hover {
    text-decoration: underline;
  }
  .fixes {
    display: flex;
    flex-wrap: wrap;
    gap: 4px 12px;
    margin-top: 2px;
  }
  .tipq {
    display: inline-flex;
    align-items: center;
    gap: 3px;
    cursor: help;
    margin-left: 6px;
  }
  .sdeps,
  .deps-imp {
    display: flex;
    flex-wrap: wrap;
    gap: 6px;
    align-items: center;
    margin-top: 6px;
  }
  .chip.imp {
    border-style: dashed;
  }
  .dep-sg {
    display: flex;
    gap: 6px;
    align-items: center;
    margin-top: 6px;
    flex-wrap: wrap;
  }
  .sum dl.kvs {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 6px 12px;
    margin: 0;
    font-size: var(--fs-sm);
  }
  .sum dt {
    color: var(--text-3);
  }
  .sum dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .mnt + .mnt {
    margin-top: 4px;
  }
  .srv {
    overflow-wrap: anywhere;
  }
  code.sec {
    background: var(--amber-soft);
  }
  .card-head h3 {
    margin: 0;
    margin-right: auto;
  }
  @media (max-width: 900px) {
    .rh {
      display: none;
    }
    .port-grid,
    .vol-grid,
    .env-grid {
      grid-template-columns: minmax(0, 1fr);
    }
    .span2 {
      grid-column: auto;
    }
    [data-label]::before {
      content: attr(data-label);
      display: block;
      font-size: var(--fs-xs);
      color: var(--text-3);
      margin-bottom: 2px;
    }
    .c-x {
      justify-self: end;
      margin-top: -4px;
    }
  }
</style>
