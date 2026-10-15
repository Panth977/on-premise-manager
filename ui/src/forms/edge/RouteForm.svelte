<script lang="ts" module>
  import type { EdgeMatchKind, EdgeProxyAction, EdgeRedirectAction, EdgeRoute, Site } from '@opm/shared/types';
  import type { ImageLike } from '../host';
  export type RouteActionType = EdgeRoute['action']['type'];

  /** A blank route of an action type for "Add route" (id from "/" unique on the site, order after the others). */
  export function newRoute(type: RouteActionType, site: Site, image?: (serviceSlug: string, version: string) => ImageLike | undefined): EdgeRoute {
    const taken = site.config.edge.routes.map((r) => r.routeId);
    return { routeId: suggestRouteId(type === 'proxy' ? 'route' : type, taken), hosts: [], match: { kind: 'prefix', path: '/', stopRegex: false, methods: null }, action: newAction(type, site, image), order: nextOrder(site.config.edge.routes, 'prefix'), responseHeaders: null };
  }
  export function newAction(type: RouteActionType, site: Site, image?: (serviceSlug: string, version: string) => ImageLike | undefined): EdgeRoute['action'] {
    if (type === 'proxy') {
      const c0 = site.config.containers[0];
      return newProxyAction(c0?.containerSlug ?? '', c0 ? firstEdgePort(site, c0.containerSlug, image) : '');
    }
    if (type === 'redirect') return { type: 'redirect', to: '', status: 301, keepPath: false };
    return { type: 'rewrite', regex: '^(.*)$', replacement: '$1' };
  }
  /** the first image port of a container the edge may proxy ('' when none) */
  export function firstEdgePort(site: Site, containerSlug: string, image?: (serviceSlug: string, version: string) => ImageLike | undefined): string {
    const c = site.config.containers.find((x) => x.containerSlug === containerSlug);
    if (!c || !image) return '';
    const ports = image(c.serviceSlug, c.version)?.ports ?? [];
    return ports.find((p) => c.ports.find((x) => x.portName === p.name)?.edge !== false)?.name ?? '';
  }
  /** The route as stored: the consistency rules of both GUIs (trimmed path, stop regex only on prefix, methods null = all …). */
  export function normalizeRoute(r: EdgeRoute): EdgeRoute {
    const kind = r.match.kind;
    const a = r.action;
    const action: EdgeRoute['action'] =
      a.type === 'proxy'
        ? { ...a, stripPrefix: kind === 'regex' ? false : a.stripPrefix, addPrefix: a.addPrefix?.trim() ? a.addPrefix.trim() : null }
        : a.type === 'redirect'
          ? { ...a, keepPath: kind === 'prefix' ? a.keepPath : false }
          : a;
    return {
      ...r,
      routeId: r.routeId.trim(),
      match: { kind, path: kind === 'regex' ? r.match.path : r.match.path.trim(), stopRegex: kind === 'prefix' ? r.match.stopRegex : false, methods: r.match.methods?.length ? EDGE_METHODS.filter((m) => r.match.methods!.includes(m)) : null },
      action,
      responseHeaders: a.type === 'rewrite' ? null : r.responseHeaders,
    };
  }
  /** Write an edited route into the site (replaces the original by its id, else appends). Same rule in both GUIs. */
  export function applyRoute(site: Site, original: EdgeRoute | null, next: EdgeRoute) {
    const r = normalizeRoute(next);
    const list = site.config.edge.routes;
    const i = original ? list.findIndex((x) => x.routeId === original.routeId) : -1;
    if (i >= 0) list[i] = r;
    else list.push(r);
  }
  export type { EdgeMatchKind, EdgeProxyAction, EdgeRedirectAction };
</script>

<script lang="ts">
  /**
   * Edge route form (SPEC §12, §17 / §18 Edge; §21 #102) — ONE page form for cloud add / edit, agent read and agent local
   * edit. FormPage with numbered sections 1 Match · 2 Action · 3 Connection (proxy) · 4 Request headers → container
   * (proxy) · 5 Response headers → browser (not rewrite), aside: Where it sits · Try a path · Summary · Validation.
   * The caller owns the page header, Cancel / Apply and calls `applyRoute(site, original, value)`; `valid` (bindable) =
   * no errors. Errors of untouched fields show after the first Apply attempt (`submitted`) or in edit mode; warnings and
   * regex errors always show.
   */
  import type { EdgeResponseHeaders, ValidationIssue, Config } from '@opm/shared/types';
  import { EDGE_METHODS, containerPorts, edgeExprContext, imagePortNumber, moveToResponseHeaders, newProxyAction, varValues } from '@opm/shared/validate';
  import { EDGE_DEFAULTS, checkEdgeRegex, effectiveRouteOrder, emptyResponseHeaders, resolveEdgeRequest } from '@opm/shared/edge-resolve';
  import Field from '../../primitives/Field.svelte';
  import FormPage from '../../primitives/FormPage.svelte';
  import FormSection from '../../primitives/FormSection.svelte';
  import SegmentedControl from '../../primitives/SegmentedControl.svelte';
  import Select from '../../primitives/Select.svelte';
  import MultiSelect from '../../primitives/MultiSelect.svelte';
  import NumberStepper from '../../primitives/NumberStepper.svelte';
  import Toggle from '../../primitives/Toggle.svelte';
  import ExprInput from '../../primitives/ExprInput.svelte';
  import Issues from '../../primitives/Issues.svelte';
  import Btn from '../../primitives/Btn.svelte';
  import Icon from '../../primitives/Icon.svelte';
  import ByteSizeInput from './ByteSizeInput.svelte';
  import RequestHeadersEditor from './RequestHeadersEditor.svelte';
  import ResponseHeadersEditor from './ResponseHeadersEditor.svelte';
  import { useConfigHost, issuesUnder, type FormMode } from '../host';
  import { GROUP_TONE, bytesText, exampleUrl, groupLabel, inheritHost, nextOrder, portMaps, resolvedEdge, suggestRouteId } from './edge';

  interface Props {
    value: EdgeRoute;
    site: Site;
    /** the route being edited (null = add) */
    original: EdgeRoute | null;
    mode: FormMode;
    submitted?: boolean;
    valid?: boolean;
    /** the number of errors (also shown as "n errors to fix first" in the aside) */
    errorCount?: number;
    /** short feedback after a one-click action (moved header, preset) — e.g. the app's toast */
    onnotice?: (text: string) => void;
  }
  let { value = $bindable(), site, original, mode, submitted = false, valid = $bindable(true), errorCount = $bindable(0), onnotice }: Props = $props();
  const host = useConfigHost();
  const ro = $derived(mode === 'read' || host.readOnly);
  const slug = $derived(site.siteSlug);

  // ───── route id follows the path while adding, until the id is typed
  // svelte-ignore state_referenced_locally
  let manualId = $state(mode !== 'create');
  const taken = $derived(site.config.edge.routes.filter((r) => r.routeId !== original?.routeId).map((r) => r.routeId));
  $effect(() => {
    if (manualId || ro) return;
    const base = value.match.path === '/' || !value.match.path.trim() ? (value.action.type === 'proxy' ? value.action.containerSlug || 'root' : value.action.type) : value.match.path;
    const id = suggestRouteId(base, taken);
    if (id !== value.routeId) value.routeId = id;
  });

  // ───── candidate config + validation
  const ctx = $derived(edgeExprContext(host.config(), host.deploymentId, slug));
  const candSite = $derived.by<Site>(() => {
    const s = structuredClone($state.snapshot(site)) as Site;
    const next = normalizeRoute($state.snapshot(value) as EdgeRoute);
    const i = original ? s.config.edge.routes.findIndex((r) => r.routeId === original.routeId) : -1;
    if (i >= 0) s.config.edge.routes[i] = next;
    else s.config.edge.routes.push(next);
    return s;
  });
  const candidate = $derived.by<Config>(() => {
    const cfg = host.config();
    return { ...cfg, sites: cfg.sites.map((x) => (x.siteSlug === slug ? candSite : x)) };
  });
  const prefix = $derived(`sites[${slug}].config.edge.routes[${value.routeId.trim()}]`);
  let reqErrs = $state<string[]>([]);
  let respErrs = $state<string[]>([]);
  const dupe = $derived(!!value.routeId.trim() && taken.includes(value.routeId.trim()));
  const regexErr = $derived(value.match.kind === 'regex' && value.match.path ? checkEdgeRegex(value.match.path) : null);
  const pxRegexErr = $derived(value.action.type === 'proxy' && value.action.rewrite?.regex ? checkEdgeRegex(value.action.rewrite.regex) : null);
  const rwRegexErr = $derived(value.action.type === 'rewrite' && value.action.regex ? checkEdgeRegex(value.action.regex) : null);
  const iss = $derived(ro ? [] : issuesUnder(host.validate(candidate), prefix));
  const local = $derived<ValidationIssue[]>(
    ro
      ? []
      : [
          ...(!value.routeId.trim() ? [{ path: `${prefix}.routeId`, code: 'V_REQUIRED', message: 'route id is required', severity: 'error' as const }] : []),
          ...(dupe ? [{ path: `${prefix}.routeId`, code: 'V_DUPLICATE', message: 'route id is used by another route', severity: 'error' as const }] : []),
          ...(value.action.type === 'proxy' ? reqErrs.map((m) => ({ path: `${prefix}.action.setHeaders`, code: 'V_FORMAT', message: m, severity: 'error' as const })) : []),
          ...(value.action.type !== 'rewrite' && value.responseHeaders ? respErrs.map((m) => ({ path: `${prefix}.responseHeaders`, code: 'V_EDGE_HEADER', message: m, severity: 'error' as const })) : []),
        ],
  );
  const all = $derived([...local, ...iss]);
  const errs = $derived(all.filter((i) => i.severity === 'error'));
  $effect(() => {
    valid = !ro && errs.length === 0;
    errorCount = errs.length;
  });
  const show = $derived(submitted || mode === 'edit');
  /** issues of one field: errors once shown, warnings always */
  const at = (p: string, deep = false): ValidationIssue[] => all.filter((i) => (i.path === `${prefix}.${p}` || (deep && (i.path.startsWith(`${prefix}.${p}.`) || i.path.startsWith(`${prefix}.${p}[`)))) && (show || i.severity !== 'error'));
  const shownList = $derived(show ? all : all.filter((i) => i.severity !== 'error' || i.code === 'V_EDGE_REGEX'));

  // ───── match
  const KINDS = [
    { value: 'prefix' as const, label: 'Prefix', title: 'The path starts with … (nginx location /x/)' },
    { value: 'exact' as const, label: 'Exact', title: 'The path equals … (nginx location = /x)' },
    { value: 'regex' as const, label: 'Regex', title: 'A regular expression (nginx location ~ / ~*)' },
  ];
  function setKind(k: EdgeMatchKind) {
    value.match.kind = k;
    if (k !== 'prefix') value.match.stopRegex = false;
    const a = value.action;
    if (a.type === 'redirect' && k !== 'prefix') a.keepPath = false;
    if (a.type === 'proxy' && k === 'regex' && a.stripPrefix) {
      a.stripPrefix = false;
      a.rewrite = { regex: value.match.path.startsWith('^') ? value.match.path : '^(.*)$', replacement: '$1' };
    }
    if (k === 'regex') value.order = original?.match.kind === 'regex' ? original.order : nextOrder(site.config.edge.routes.filter((x) => x.routeId !== original?.routeId), 'regex');
  }
  const ci = $derived(value.match.kind === 'regex' && value.match.path.startsWith('(?i)'));
  function setCi(on: boolean) {
    const body = value.match.path.replace(/^\(\?i\)/, '');
    value.match.path = on ? `(?i)${body}` : body;
  }
  const hostOpts = $derived(site.config.edge.hosts.map((h) => ({ value: h.hostname, label: h.hostname, icon: 'globe' })));
  const methods = $derived(value.match.methods ?? []);
  function toggleMethod(m: string) {
    const next = methods.includes(m) ? methods.filter((x) => x !== m) : [...methods, m];
    value.match.methods = next.length ? EDGE_METHODS.filter((x) => next.includes(x)) : null;
  }

  // ───── action
  /** per action type: the last action of that type (switching back restores it) */
  let stash: Partial<Record<RouteActionType, EdgeRoute['action']>> = {};
  const ACTS = [
    { value: 'proxy' as const, label: 'Proxy', title: 'Send the request to a container port (nginx proxy_pass)' },
    { value: 'redirect' as const, label: 'Redirect', title: 'Answer with a redirect (nginx return 301 …)' },
    { value: 'rewrite' as const, label: 'Rewrite', title: 'Change the URL internally and match the routes again (nginx rewrite … last)' },
  ];
  function setType(t: RouteActionType) {
    if (value.action.type === t) return;
    stash[value.action.type] = $state.snapshot(value.action) as EdgeRoute['action'];
    value.action = stash[t] ?? newAction(t, site, host.image);
    if (t === 'rewrite') value.responseHeaders = null;
  }
  const pa = $derived(value.action.type === 'proxy' ? value.action : null);
  const ra = $derived(value.action.type === 'redirect' ? value.action : null);
  const wa = $derived(value.action.type === 'rewrite' ? value.action : null);
  const container = $derived(pa ? (site.config.containers.find((c) => c.containerSlug === pa.containerSlug) ?? null) : null);
  const img = $derived(container ? host.image(container.serviceSlug, container.version) : undefined);
  const edgeOf = $derived<Record<string, boolean>>(container && img ? Object.fromEntries(containerPorts(container, img).map((p) => [p.portName, p.edge])) : {});
  const vars = $derived(varValues(host.config()));
  const containerOpts = $derived(
    site.config.containers.map((c) => {
      const svc = host.service(c.serviceSlug);
      return { value: c.containerSlug, label: c.containerSlug, hint: `${c.serviceSlug}:${c.version}${svc?.label ? ` · ${svc.label}` : ''}`, badge: c.enabled ? undefined : { text: 'disabled', tone: 'amber' as const } };
    }),
  );
  const portOpts = $derived(
    (img?.ports ?? []).map((p) => ({
      value: p.name,
      label: p.name,
      hint: `${imagePortNumber(p, vars, host.deploymentId) ?? p.port}/${p.protocol}${p.description ? ` · ${p.description}` : ''}`,
      badge: edgeOf[p.name] === false ? { text: 'edge off', tone: 'amber' as const } : undefined,
      disabled: edgeOf[p.name] === false,
    })),
  );
  function setContainer(v: string) {
    if (!pa) return;
    pa.containerSlug = v;
    pa.portName = firstEdgePort(site, v, host.image);
  }
  type PathMode = 'keep' | 'strip' | 'rewrite';
  const pathMode = $derived<PathMode>(pa ? (pa.rewrite ? 'rewrite' : pa.stripPrefix ? 'strip' : 'keep') : 'keep');
  const escRe = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const PATH_MODES = $derived([
    { value: 'keep' as PathMode, label: 'Keep path', title: 'The container gets the request path unchanged' },
    { value: 'strip' as PathMode, label: 'Strip prefix', title: value.match.kind === 'regex' ? 'Only for prefix / exact routes: use Rewrite' : '/x/app/a → /a (the matched path is removed)', disabled: value.match.kind === 'regex' },
    { value: 'rewrite' as PathMode, label: 'Rewrite (regex)', title: 'nginx rewrite … break: regex → replacement ($1…$9)' },
  ]);
  function setPathMode(m: PathMode) {
    if (!pa) return;
    if (m === 'keep') {
      pa.stripPrefix = false;
      pa.rewrite = null;
    } else if (m === 'strip') {
      pa.stripPrefix = true;
      pa.rewrite = null;
    } else {
      pa.stripPrefix = false;
      const p = value.match.path;
      pa.rewrite = pa.rewrite ?? { regex: value.match.kind === 'regex' ? p : value.match.kind === 'prefix' && p !== '/' ? `^${escRe(p.replace(/\/$/, ''))}(/.*)$` : '^(.*)$', replacement: '$1' };
    }
  }
  const STATUSES = [
    { value: 301 as const, label: '301', title: 'Moved permanently (method may change) — nginx return 301' },
    { value: 302 as const, label: '302', title: 'Found (method may change)' },
    { value: 303 as const, label: '303', title: 'See other (always GET)' },
    { value: 307 as const, label: '307', title: 'Temporary, keeps the method' },
    { value: 308 as const, label: '308', title: 'Permanent, keeps the method' },
  ];
  const VARS = ['$uri', '$args', '$is_args', '$request_uri', '$host', '$scheme', '$1…$9', '{path}', '{query}', '{host}'];

  // ───── connection (streaming / timeouts)
  let streamOpen = $state(false);
  let timeoutsOpen = $state(false);
  $effect(() => {
    if (pa && (pa.requestBuffering || pa.responseBuffering || pa.maxBodyBytes !== null)) streamOpen = true;
  });
  $effect(() => {
    if (pa && (pa.connectTimeoutSec !== EDGE_DEFAULTS.connectTimeoutSec || pa.sendTimeoutSec !== EDGE_DEFAULTS.sendTimeoutSec || pa.readTimeoutSec !== EDGE_DEFAULTS.readTimeoutSec)) timeoutsOpen = true;
  });
  function presetSse() {
    if (!pa) return;
    pa.requestBuffering = false;
    pa.responseBuffering = false;
    streamOpen = true;
    timeoutsOpen = true;
    onnotice?.('SSE: buffering off both ways. Keep the read timeout above the longest pause between two events.');
  }
  function presetUploads() {
    if (!pa) return;
    pa.requestBuffering = false;
    if (pa.maxBodyBytes === null) pa.maxBodyBytes = 1024 * 1024 * 1024;
    streamOpen = true;
    onnotice?.(`Uploads: request body streamed; max body ${bytesText(pa.maxBodyBytes === 0 ? null : pa.maxBodyBytes)}.`);
  }

  // ───── headers
  const iHost = $derived(inheritHost(site.config.edge, value));
  function moveHeader(name: string) {
    value = moveToResponseHeaders($state.snapshot(value) as EdgeRoute, name, iHost ? ($state.snapshot(iHost) as typeof iHost) : null);
    respErrs = [];
    onnotice?.(`${name} moved to “Response headers → browser” (set)`);
  }
  function setRespOwn(own: boolean) {
    if (own === !!value.responseHeaders) return;
    value.responseHeaders = own ? (structuredClone($state.snapshot(iHost?.responseHeaders ?? emptyResponseHeaders())) as EdgeResponseHeaders) : null;
    respErrs = [];
  }

  // ───── try a path (shared resolver on the resolved candidate edge)
  let example = $state('');
  let exampleTyped = $state(false);
  const defaultExample = $derived(value.match.kind === 'exact' ? value.match.path : value.match.kind === 'prefix' ? (value.match.path.endsWith('/') ? `${value.match.path}example` : `${value.match.path}/example`) : '/');
  const samplePath = $derived(exampleTyped ? example : defaultExample);
  const pEdge = $derived(resolvedEdge(candSite.config.edge, ctx));
  const pMethod = $derived(value.match.methods?.[0] ?? 'GET');
  const previewUrl = $derived(samplePath.trim() ? exampleUrl(pEdge, inheritHost(pEdge, value), samplePath.trim()) : null);
  const preview = $derived(previewUrl ? resolveEdgeRequest(pEdge, previewUrl, { method: pMethod, ports: portMaps(host, candSite) }) : null);
  const mine = $derived(!!preview && (preview.route?.routeId === value.routeId.trim() || preview.rewrites.some((r) => r.routeId === value.routeId.trim())));

  // ───── where it sits
  const orderList = $derived(effectiveRouteOrder(candSite.config.edge.routes.filter((r) => !value.hosts.length || !r.hosts.length || r.hosts.some((h) => value.hosts.includes(h)))));
  const sections = $derived([
    { id: 'sec-match', label: 'Match', badge: at('match', true).filter((i) => i.severity === 'error').length || null, tone: 'red' as const },
    { id: 'sec-action', label: 'Action', badge: at('action', true).filter((i) => i.severity === 'error').length || null, tone: 'red' as const },
    ...(pa ? [{ id: 'sec-stream', label: 'Connection' }, { id: 'sec-req', label: 'Request headers' }] : []),
    ...(!wa ? [{ id: 'sec-resp', label: 'Response headers' }] : []),
  ]);
</script>

<fieldset class="form" disabled={ro} data-testid="route-form">
  <FormPage {sections} testid="route-editor">
    <FormSection n={1} title="Match" sub="Which requests this route answers (nginx location)" id="sec-match" testid="sec-match">
      <div class="stack">
        <Field label="Match kind" id="r-mk" group hint={KINDS.find((k) => k.value === value.match.kind)?.title} issues={at('match.kind')}>
          <SegmentedControl id="r-mk" options={KINDS} value={value.match.kind} onchange={setKind} disabled={ro} ariaLabel="Match kind" testid="route-match-kind" />
        </Field>
        <div class="grid-2">
          <Field
            label={value.match.kind === 'regex' ? 'Regular expression' : 'Path'}
            id="r-path"
            required
            hint={value.match.kind === 'prefix' ? 'Plain string prefix like nginx: "/api" also matches /apix — end with "/" for a folder' : value.match.kind === 'exact' ? 'Only exactly this path (no query)' : 'RE2 syntax that JavaScript reads the same way; matched against the decoded path without the query; $1…$9 usable in the target. No lookaround or backreferences.'}
            error={regexErr}
            issues={at('match.path')}
          >
            <input id="r-path" class="input mono" bind:value={value.match.path} placeholder={value.match.kind === 'regex' ? '^/apis/(docs|_docs_/[^/]+)$' : '/api/'} disabled={ro} aria-invalid={regexErr ? 'true' : undefined} data-testid="route-path" />
          </Field>
          <Field label="Route id" id="r-id" required hint={manualId ? 'Unique on the site' : 'From the path; type to set your own'} issues={at('routeId')}>
            <input id="r-id" class="input mono" value={value.routeId} oninput={(e) => ((manualId = true), (value.routeId = e.currentTarget.value.toLowerCase()))} maxlength="63" placeholder="api" disabled={ro} data-testid="route-id" />
          </Field>
        </div>
        {#if value.match.kind === 'regex'}
          <Toggle checked={ci} onchange={setCi} disabled={ro} label="Case-insensitive (nginx ~*)" testid="regex-ci" />
        {/if}
        {#if value.match.kind === 'prefix'}
          <Field label="Stop regex (nginx ^~)" id="r-stop" hint="When this is the LONGEST matching prefix, regex routes are not checked and this route wins." issues={at('match.stopRegex')}>
            <Toggle bind:checked={value.match.stopRegex} disabled={ro} label={value.match.stopRegex ? 'on — regex routes skipped' : 'off'} testid="route-stop-regex" />
          </Field>
        {/if}
        <div class="grid-2">
          <Field label="Hosts" id="r-hosts" hint="Empty = every host of the edge" issues={at('hosts', true)}>
            <MultiSelect id="r-hosts" options={hostOpts} bind:values={value.hosts} placeholder="all hosts" allLabel="All hosts" disabled={ro} ariaLabel="Hosts" testid="route-hosts" />
          </Field>
          <Field label="Methods" group hint={methods.length ? 'Other methods do not see this route' : 'None picked = every method'} issues={at('match.methods')}>
            <div class="chips" role="group" aria-label="Methods" data-testid="route-methods">
              {#each EDGE_METHODS as m (m)}
                <button type="button" class="mchip" class:on={methods.includes(m)} aria-pressed={methods.includes(m)} disabled={ro} onclick={() => toggleMethod(m)} data-testid="method-{m}">{m}</button>
              {/each}
            </div>
          </Field>
        </div>
      </div>
    </FormSection>

    <FormSection n={2} title="Action" id="sec-action" testid="sec-action">
      <div class="stack">
        <Field label="Action" id="r-kind" group hint={ACTS.find((a) => a.value === value.action.type)?.title}>
          <SegmentedControl id="r-kind" options={ACTS} value={value.action.type} onchange={setType} disabled={ro} ariaLabel="Route action" testid="route-kind" />
        </Field>

        {#if pa}
          <div class="grid-2">
            <Field label="Target container" id="r-svc" required issues={at('action.containerSlug')}>
              <Select id="r-svc" value={pa.containerSlug} options={containerOpts} mono placeholder={containerOpts.length ? 'container of this site' : 'No containers on this site'} disabled={ro} ariaLabel="Target container" testid="route-service" onchange={setContainer} />
            </Field>
            <Field label="Port" id="r-port" required issues={at('action.portName')} hint={edgeOf[pa.portName] === false ? 'The edge may not proxy this port: turn on “Edge may proxy” in the container' : undefined}>
              <Select id="r-port" value={pa.portName} options={portOpts} mono placeholder={portOpts.length ? 'image port' : 'The image has no ports'} fallback={pa.portName || undefined} disabled={ro} ariaLabel="Target port" testid="route-port" onchange={(v) => pa && (pa.portName = v)} />
            </Field>
          </div>
          <Field label="Path to the container" id="r-pm" group hint={PATH_MODES.find((m) => m.value === pathMode)?.title} issues={[...at('action.stripPrefix'), ...at('action.rewrite')]}>
            <SegmentedControl id="r-pm" options={PATH_MODES} value={pathMode} disabled={ro} ariaLabel="Path handling" testid="route-path-mode" onchange={setPathMode} />
          </Field>
          {#if pa.rewrite}
            <div class="grid-2">
              <Field label="Regex" id="r-pxre" required hint="Against the path (no query); no match → the path is unchanged" error={pxRegexErr} issues={at('action.rewrite.regex')}>
                <input id="r-pxre" class="input mono" bind:value={pa.rewrite.regex} placeholder="^/x/app(/.*)$" disabled={ro} data-testid="route-rewrite-regex" />
              </Field>
              <Field label="Replacement" id="r-pxrp" required hint={'$1…$9 + variables; a "?" replaces the query'} issues={at('action.rewrite.replacement')}>
                <input id="r-pxrp" class="input mono" bind:value={pa.rewrite.replacement} placeholder="$1" disabled={ro} data-testid="route-rewrite-replacement" />
              </Field>
            </div>
          {/if}
          <Field label="Add prefix" id="r-add" hint="Prepended to the upstream path, e.g. /v2" issues={at('action.addPrefix')}>
            <input id="r-add" class="input mono" value={pa.addPrefix ?? ''} oninput={(e) => pa && (pa.addPrefix = e.currentTarget.value || null)} placeholder="none" disabled={ro} data-testid="route-add-prefix" />
          </Field>
        {:else if ra}
          <Field label="To" id="r-tol" required hint="URL or path; expressions allowed" issues={at('action.to')}>
            <ExprInput id="r-tol" bind:value={ra.to} field="edge" {ctx} placeholder={'$uri/$is_args$args'} disabled={ro} testid="route-to" />
          </Field>
          {#if !ro}
            <div class="vars" data-testid="route-vars">
              <span class="hint">Variables:</span>
              {#each VARS as v (v)}<button type="button" class="mchip var" onclick={() => ra && (ra.to = ra.to + (v === '$1…$9' ? '$1' : v))} title="Insert {v}">{v}</button>{/each}
              <span class="hint">$1…$9 = captures of a regex route</span>
            </div>
          {/if}
          <div class="grid-2">
            <Field label="Status" id="r-st" group hint={ra.status === 301 || ra.status === 308 ? 'permanent (browsers cache it)' : 'temporary'} issues={at('action.status')}>
              <SegmentedControl id="r-st" options={STATUSES} value={ra.status} onchange={(v) => ra && (ra.status = v as EdgeRedirectAction['status'])} disabled={ro} ariaLabel="Redirect status" testid="route-status" />
            </Field>
            <Field label="Keep path" id="r-kp" hint={value.match.kind === 'prefix' ? 'Append the rest after the prefix + the query' : 'Prefix routes only — use $uri / $1 in “To”'} issues={at('action.keepPath')}>
              <Toggle bind:checked={ra.keepPath} disabled={ro || value.match.kind !== 'prefix'} label={ra.keepPath ? 'on' : 'off'} testid="route-keep-path" />
            </Field>
          </div>
        {:else if wa}
          <div class="banner info small" data-testid="rewrite-explain">
            <Icon name="info" size={14} />
            <div><strong>Internal:</strong> the URL is changed and the routes are matched again (nginx <code>rewrite … last</code>). The browser sees no redirect; more than 10 rewrites → 500. To send the request to a container with another path, use a <em>Proxy</em> route with “Rewrite (regex)” instead.</div>
          </div>
          <div class="grid-2">
            <Field label="Regex" id="r-rwre" required hint="Against the current path; no match → 404" error={rwRegexErr} issues={at('action.regex')}>
              <input id="r-rwre" class="input mono" bind:value={wa.regex} placeholder="^" disabled={ro} data-testid="route-rw-regex" />
            </Field>
            <Field label="Replacement (new URL)" id="r-rwrp" required hint={'$1…$9 + variables, e.g. /x/nodered$request_uri; "?" replaces the query'} issues={at('action.replacement')}>
              <input id="r-rwrp" class="input mono" bind:value={wa.replacement} placeholder="/x/nodered$request_uri" disabled={ro} data-testid="route-rw-replacement" />
            </Field>
          </div>
        {/if}
      </div>
    </FormSection>

    {#if pa}
      <FormSection n={3} title="Connection" sub="Streaming, body size, timeouts, WebSocket" id="sec-stream" testid="sec-stream">
        <div class="stack">
          <div class="row-w">
            <Toggle bind:checked={pa.websocket} disabled={ro} label={pa.websocket ? 'WebSocket allowed' : 'WebSocket refused (400)'} testid="route-ws" />
            <span class="hint">On by default; the edge handles the Upgrade itself (no Connection / Upgrade headers needed).</span>
          </div>
          <details class="sec" bind:open={streamOpen} data-testid="sec-streaming">
            <summary data-testid="sec-streaming-toggle"><Icon name="activity" size={14} />Streaming (SSE / uploads)<span class="hint">{pa.requestBuffering ? 'request buffered' : 'request streamed'} · {pa.responseBuffering ? 'response buffered' : 'response flushed'} · body {pa.maxBodyBytes === null ? `edge (${bytesText(site.config.edge.maxBodyBytes)})` : bytesText(pa.maxBodyBytes === 0 ? null : pa.maxBodyBytes)}</span></summary>
            <div class="stack secb">
              {#if !ro}
                <div class="row-w">
                  <span class="hint">Presets</span>
                  <Btn size="sm" icon="activity" onclick={presetSse} data-testid="preset-sse" title="Server-sent events: no buffering either way">SSE / live events</Btn>
                  <Btn size="sm" icon="upload" onclick={presetUploads} data-testid="preset-uploads" title="Large uploads: stream the body + a size limit">Uploads</Btn>
                </div>
              {/if}
              <div class="grid-2">
                <Field label="Request buffering" hint="On: the edge reads up to 8 MiB of the body before contacting the container (nginx proxy_request_buffering on). Off: streams." issues={at('action.requestBuffering')}>
                  <Toggle bind:checked={pa.requestBuffering} disabled={ro} label={pa.requestBuffering ? 'buffer' : 'stream'} testid="route-req-buffering" />
                </Field>
                <Field label="Response buffering" hint="Off: every write is flushed to the browser at once (SSE). text/event-stream always flushes." issues={at('action.responseBuffering')}>
                  <Toggle bind:checked={pa.responseBuffering} disabled={ro} label={pa.responseBuffering ? 'buffer' : 'flush at once'} testid="route-resp-buffering" />
                </Field>
              </div>
              <Field label="Max body size" group hint="Bigger request bodies get 413 before the container is contacted (nginx client_max_body_size)" issues={at('action.maxBodyBytes')}>
                <ByteSizeInput bind:value={pa.maxBodyBytes} kind="route" inheritText={bytesText(site.config.edge.maxBodyBytes)} disabled={ro} testid="route-max-body" />
              </Field>
            </div>
          </details>
          <details class="sec" bind:open={timeoutsOpen} data-testid="sec-timeouts">
            <summary data-testid="sec-timeouts-toggle"><Icon name="clock" size={14} />Timeouts<span class="hint">connect {pa.connectTimeoutSec} s · send {pa.sendTimeoutSec} s · read {pa.readTimeoutSec} s</span></summary>
            <div class="grid-3 secb">
              <Field label="Connect" id="r-tc" hint={`Dial the container; 1–${EDGE_DEFAULTS.maxConnectTimeoutSec} s`} issues={at('action.connectTimeoutSec')}>
                <NumberStepper id="r-tc" value={pa.connectTimeoutSec} onchange={(v) => pa && (pa.connectTimeoutSec = v ?? NaN)} min={1} max={EDGE_DEFAULTS.maxConnectTimeoutSec} step={5} suffix="s" disabled={ro} label="Connect timeout" testid="route-connect-timeout" />
              </Field>
              <Field label="Send" id="r-ts" hint="Idle time between two writes to the container" issues={at('action.sendTimeoutSec')}>
                <NumberStepper id="r-ts" value={pa.sendTimeoutSec} onchange={(v) => pa && (pa.sendTimeoutSec = v ?? NaN)} min={1} max={EDGE_DEFAULTS.maxTimeoutSec} step={5} suffix="s" disabled={ro} label="Send timeout" testid="route-send-timeout" />
              </Field>
              <Field label="Read" id="r-tr" hint="Time to the response header AND idle time between two reads — an SSE stream / WebSocket is not cut while data flows" issues={at('action.readTimeoutSec')}>
                <NumberStepper id="r-tr" value={pa.readTimeoutSec} onchange={(v) => pa && (pa.readTimeoutSec = v ?? NaN)} min={1} max={EDGE_DEFAULTS.maxTimeoutSec} step={5} suffix="s" disabled={ro} label="Read timeout" testid="route-read-timeout" />
              </Field>
            </div>
          </details>
        </div>
      </FormSection>

      <FormSection n={4} title="Request headers → container" id="sec-req" testid="sec-req-headers">
        <RequestHeadersEditor set={pa.setHeaders} remove={pa.removeHeaders} {ctx} disabled={ro} removeIssues={at('action.removeHeaders', true)} onmove={moveHeader} onchange={(s, r, e) => pa && ((pa.setHeaders = s), (pa.removeHeaders = r), (reqErrs = e))} />
        {#each at('action.setHeaders', true) as i, k (k)}<div class={i.severity === 'error' ? 'field-err' : 'field-warn'}><span class="issue-code">{i.code}</span>{i.message}</div>{/each}
      </FormSection>
    {/if}

    {#if !wa}
      <FormSection n={pa ? 5 : 3} title="Response headers → browser" id="sec-resp" testid="sec-resp-headers">
        <div class="stack">
          <p class="hint">nginx <code>add_header</code> (add), <code>proxy_hide_header</code> (remove), both = set. Applied to 2xx / 3xx answers unless “always”.</p>
          <SegmentedControl
            options={[
              { value: 'inherit' as const, label: `Inherit from host${iHost ? ` ${iHost.hostname}` : ''}`, title: 'This route uses the host’s list' },
              { value: 'own' as const, label: 'Own list for this route', title: 'Replaces the host’s list, like nginx (the host’s headers are NOT added)' },
            ]}
            value={value.responseHeaders ? 'own' : 'inherit'}
            disabled={ro}
            ariaLabel="Response headers source"
            testid="route-resp-mode"
            onchange={(v) => setRespOwn(v === 'own')}
          />
          {#if !value.responseHeaders}
            <div class="inh" data-testid="resp-inherited">
              {#if !iHost || (!Object.keys(iHost.responseHeaders.set).length && !Object.keys(iHost.responseHeaders.add).length && !iHost.responseHeaders.remove.length)}
                <span class="hint">The host adds no response headers.</span>
              {:else}
                {#each Object.entries(iHost.responseHeaders.set) as [k, v] (k)}<div class="mono small"><span class="badge blue">set</span> {k}: {v}{iHost.responseHeaders.always.includes(k) ? ' · always' : ''}</div>{/each}
                {#each Object.entries(iHost.responseHeaders.add) as [k, v] (k)}<div class="mono small"><span class="badge green">add</span> {k}: {v}{iHost.responseHeaders.always.includes(k) ? ' · always' : ''}</div>{/each}
                {#each iHost.responseHeaders.remove as k (k)}<div class="mono small"><span class="badge red">remove</span> {k}</div>{/each}
              {/if}
              {#if site.config.edge.hosts.length > 1 && !value.hosts.length}<div class="hint">Other hosts: each host’s own list applies.</div>{/if}
            </div>
          {:else}
            <div class="field-warn">This list REPLACES the host’s list for this route (nginx: an add_header in a location drops the server’s add_header).</div>
            <ResponseHeadersEditor value={value.responseHeaders} {ctx} disabled={ro} testid="route-resp" onchange={(h, e) => ((value.responseHeaders = h), (respErrs = e))} />
            {#each at('responseHeaders', true) as i, k (k)}<div class={i.severity === 'error' ? 'field-err' : 'field-warn'}><span class="issue-code">{i.code}</span>{i.message}</div>{/each}
          {/if}
        </div>
      </FormSection>
    {:else}
      <p class="hint">A rewrite route has no response of its own: response headers come from the route it rewrites to.</p>
    {/if}

    {#snippet aside()}
      <section class="card" data-testid="route-order">
        <div class="card-head"><h3><Icon name="list" size={15} />Where it sits</h3><span class="hint">effective order</span></div>
        <div class="card-body">
          <ol class="ord">
            {#each orderList as o, oi (oi)}
              <li class:me={o.routeId === value.routeId.trim()} title={o.why}>
                <span class="badge {GROUP_TONE[o.group]}">{groupLabel(o, orderList)}</span>
                <span class="mono small">{o.routeId === value.routeId.trim() ? value.routeId || '(this route)' : o.routeId}</span>
                <span class="mono small faint ell">{candSite.config.edge.routes.find((r) => r.routeId === o.routeId)?.match.path}</span>
              </li>
            {/each}
          </ol>
          <p class="hint">Exact first, then the longest prefix — regex routes are checked in their order unless that prefix is “stop regex”.</p>
        </div>
      </section>
      <section class="card" data-testid="route-preview">
        <div class="card-head"><h3><Icon name="play" size={15} />Try a path</h3></div>
        <div class="card-body stack tight">
          <div class="row-w">
            <span class="badge outline mono">{pMethod}</span>
            <input class="input sm mono grow" value={samplePath} oninput={(e) => ((example = e.currentTarget.value), (exampleTyped = true))} placeholder={value.match.kind === 'regex' ? 'a path this regex matches, e.g. /apis/docs' : '/path?query'} aria-label="Example path" data-testid="route-example" />
          </div>
          {#if !preview}
            <div class="hint">Type an example path to see what happens.</div>
          {:else}
            <div class="small faint mono url">{previewUrl}</div>
            <dl class="kv pv">
              <dt>Answer</dt>
              <dd><span class="badge {preview.status >= 200 && preview.status < 300 ? 'green' : preview.status >= 300 && preview.status < 400 ? 'blue' : 'amber'}">{preview.status || '—'}</span> {preview.title}</dd>
              <dt>Route</dt>
              <dd data-testid="route-preview-route">
                {#if !preview.route}<span class="muted">none</span>
                {:else if !mine}<span class="field-warn" data-testid="route-preview-other">another route wins: <code>{preview.route.routeId}</code>{preview.trace?.match.why ? ` (${preview.trace.match.why})` : ''}</span>
                {:else}<code>{preview.route.routeId}</code>{/if}
              </dd>
              {#if preview.rewrites.length}
                <dt>Rewrites</dt>
                <dd data-testid="route-preview-rewrites">{#each preview.rewrites as w, i (i)}<div class="mono small">{w.from} → {w.to}</div>{/each}{#if preview.route}<div class="small">lands on <code>{preview.route.routeId}</code></div>{/if}</dd>
              {/if}
              {#if preview.upstream}
                <dt>Container gets</dt>
                <dd class="mono small" data-testid="route-preview-upstream">{preview.upstream.path}</dd>
                <dd class="hint" data-testid="route-preview-upstream-url">{preview.trace?.upstreamPath?.how === 'strip' ? 'prefix stripped' : preview.trace?.upstreamPath?.how === 'rewrite' ? 'rewritten' : 'path unchanged'} · {preview.upstream.url}</dd>
              {/if}
              {#if preview.location}<dt>Location</dt><dd class="mono small" data-testid="route-preview-location">{preview.location}</dd>{/if}
            </dl>
          {/if}
        </div>
      </section>
      {#if pa}
        <section class="card">
          <div class="card-head"><h3><Icon name="server" size={15} />Summary</h3></div>
          <div class="card-body small stack tight" data-testid="route-summary">
            <div>→ <span class="mono">{pa.containerSlug || '?'}:{pa.portName || '?'}</span></div>
            <div>WebSocket {pa.websocket ? 'on' : 'off'} · request {pa.requestBuffering ? 'buffered' : 'streamed'} · response {pa.responseBuffering ? 'buffered' : 'flushed'}</div>
            <div>Timeouts {pa.connectTimeoutSec}/{pa.sendTimeoutSec}/{pa.readTimeoutSec} s · body {pa.maxBodyBytes === null ? `edge ${bytesText(site.config.edge.maxBodyBytes)}` : bytesText(pa.maxBodyBytes === 0 ? null : pa.maxBodyBytes)}</div>
            <div>{Object.keys(pa.setHeaders).length} request header{Object.keys(pa.setHeaders).length === 1 ? '' : 's'} · response headers {value.responseHeaders ? 'own list' : 'from the host'}</div>
          </div>
        </section>
      {/if}
      {#if !ro}
        <Issues issues={shownList} title="Route validation" testid="route-issues" />
        {#if !shownList.length}<div class="banner ok small" data-testid="route-valid"><Icon name="check" size={14} /><div class="grow">No problems.</div></div>{/if}
        {#if show && errs.length}<div class="field-err" data-testid="route-errors-left">{errs.length} error{errs.length === 1 ? '' : 's'} to fix first</div>{/if}
      {/if}
    {/snippet}
  </FormPage>
</fieldset>

<style>
  .chips,
  .vars {
    display: flex;
    flex-wrap: wrap;
    gap: 5px;
    align-items: center;
  }
  .mchip {
    font: inherit;
    font-size: 12px;
    font-family: var(--font-mono);
    padding: 3px 9px;
    border-radius: var(--r-full);
    border: 1px solid var(--border-strong);
    background: var(--surface);
    color: var(--text-2);
    cursor: pointer;
  }
  .mchip.on {
    background: var(--accent-soft);
    color: var(--accent-text);
    border-color: color-mix(in srgb, var(--accent) 50%, transparent);
    font-weight: var(--fw-semibold);
  }
  .mchip:disabled {
    opacity: 0.55;
    cursor: not-allowed;
  }
  .row-w {
    display: flex;
    flex-wrap: wrap;
    gap: 8px;
    align-items: center;
  }
  .sec {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
  }
  .sec summary {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    padding: 8px 12px;
    cursor: pointer;
    font-weight: var(--fw-medium);
  }
  .secb {
    padding: 4px 12px 12px;
  }
  .inh {
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .ord {
    margin: 0 0 6px;
    padding-left: 18px;
    display: flex;
    flex-direction: column;
    gap: 4px;
  }
  .ord li {
    display: flex;
    gap: 6px;
    align-items: center;
    min-width: 0;
  }
  .ord li.me {
    font-weight: var(--fw-semibold);
  }
  .ell {
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    min-width: 0;
  }
  .card-head h3 {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    margin: 0;
    font-size: var(--fs-md);
  }
  .url {
    overflow-wrap: anywhere;
  }
  .pv {
    display: grid;
    grid-template-columns: auto minmax(0, 1fr);
    gap: 3px 10px;
    margin: 0;
    font-size: var(--fs-sm);
  }
  .pv dt {
    color: var(--text-3);
  }
  .pv dd {
    margin: 0;
    min-width: 0;
    overflow-wrap: anywhere;
  }
  .pv dd.hint {
    grid-column: 2;
  }
</style>
