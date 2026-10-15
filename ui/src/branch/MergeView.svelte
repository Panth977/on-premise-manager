<script lang="ts">
  /**
   * Compare + cherry-pick (§21 #104 D11, docs-v2/branches-commits.md §4 + §10.1). ONE page body for the cloud (any two
   * branches) and the agent GUI (cloud commit → this site). Rows = merge items (field level); deletions get their own
   * group "Removed by this merge"; conflicts need a decision (Keep target · Take source · Custom for scalars); a
   * debounced preview validates the result; "Merge…" opens the CommitDialog (message required) → host.merge.
   * diffOnly = read-only compare (history, Review): no checkboxes, no preview.
   */
  import type { Snippet } from 'svelte';
  import type { MergeChoice, MergeItem, MergePlan, MergePreview, CompareRequest } from '@opm/shared/branch';
  import { cleanSourceChoices, removalChoices } from '@opm/shared/branch';
  import Btn from '../primitives/Btn.svelte';
  import Icon from '../primitives/Icon.svelte';
  import Issues from '../primitives/Issues.svelte';
  import SegmentedControl from '../primitives/SegmentedControl.svelte';
  import CommitDialog from './CommitDialog.svelte';
  import TextDiff from './TextDiff.svelte';
  import { useBranchHost, type MergeResult } from './host';
  import { dependantsOf, EFFECT_LABEL, FILTERS, groupItems, inFilter, KIND_LABEL, KIND_TONE, removalText, show, type FilterKey } from './labels';

  interface Props {
    plan?: MergePlan | null;
    /** load the plan with host.compare(request) when `plan` is not given (and again on refresh) */
    request?: CompareRequest | null;
    diffOnly?: boolean;
    /** header slot to pick the source (Compare page) */
    sourcePicker?: Snippet;
    /** after a successful merge */
    onmerged?: (r: MergeResult) => void;
    /** label of the merge button (agent: "Merge & apply…") */
    mergeLabel?: string;
    /** extra warnings for the commit dialog (needs agent ≥ 0.14 …) */
    warnings?: readonly string[];
    /** §21 #105: the browser holds N pending (not committed) changes of the target — they are rebased after the merge */
    pendingNote?: string | null;
    testid?: string;
  }
  let { plan = $bindable(null), request = null, diffOnly, sourcePicker, onmerged, mergeLabel, warnings = [], pendingNote = null, testid = 'merge-view' }: Props = $props();
  const host = useBranchHost();
  let loading = $state(false);
  let loadError = $state<string | null>(null);
  let filter = $state<FilterKey>('all');
  let picks = $state<Record<string, MergeChoice>>({});
  let expanded = $state<Record<string, boolean>>({});
  let note = $state<{ text: string; undo: () => void } | null>(null);
  let values = $state<Record<string, string>>({});
  /** deployment target: values for required secrets the merge adds (sent as secretValues, never shown back) */
  let secretInputs = $state<Record<string, string>>({});
  /** what the merge needs a value for — kept once asked, so a field does not vanish when its value is typed */
  let askVars = $state<MergePreview['missing']['variables']>([]);
  let askSecrets = $state<MergePreview['missing']['secrets']>([]);
  let preview = $state<MergePreview | null>(null);
  let previewBusy = $state(false);
  let previewError = $state<string | null>(null);
  let dialogOpen = $state(false);
  let merging = $state(false);
  let sheet = $state(false);
  let blobs = $state<Record<string, string | null | undefined>>({});

  const ro = $derived(diffOnly ?? plan?.diffOnly ?? false);
  const items = $derived(plan?.items ?? []);
  const byKey = $derived(new Map(items.map((i) => [i.key, i])));

  async function load() {
    if (!request) return;
    loading = true;
    loadError = null;
    try {
      plan = await host.compare(request);
    } catch (e) {
      loadError = e instanceof Error ? e.message : String(e);
    } finally {
      loading = false;
    }
  }
  $effect(() => {
    if (request && !plan) void load();
  });
  // initial picks = the plan's preselection (fast-forward / reset)
  let planId = '';
  $effect(() => {
    const id = plan ? `${plan.source.commit?.commitId}:${plan.target.head?.commitId}:${plan.items.length}` : '';
    if (id === planId) return;
    planId = id;
    const p: Record<string, MergeChoice> = {};
    for (const i of plan?.items ?? []) if (i.preselect && !i.locked && i.kind !== 'conflict') p[i.key] = { key: i.key, take: 'source' };
    picks = p;
    values = {};
    secretInputs = {};
    askVars = [];
    askSecrets = [];
    preview = null;
    note = null;
  });

  const choices = $derived(Object.values(picks));
  const takenItems = $derived(items.filter((i) => picks[i.key] && picks[i.key].take !== 'target'));
  const unresolved = $derived(items.filter((i) => i.kind === 'conflict' && !picks[i.key]).length);
  const removals = $derived(items.filter((i) => i.effect === 'remove'));
  const visible = $derived(items.filter((i) => i.effect !== 'remove' && inFilter(i, filter)));
  const groups = $derived(groupItems(visible));
  const showRemovals = $derived(removals.filter((i) => inFilter(i, filter)));
  const counts = $derived(Object.fromEntries(FILTERS.map((f) => [f.value, items.filter((i) => inFilter(i, f.value)).length])) as Record<FilterKey, number>);

  // ── ticking with requirements
  function closure(key: string, seen = new Set<string>()): string[] {
    const it = byKey.get(key);
    if (!it || seen.has(key)) return [];
    seen.add(key);
    const out: string[] = [];
    for (const r of it.requires) if (!picks[r] && byKey.get(r) && !byKey.get(r)!.locked) out.push(r, ...closure(r, seen));
    return [...new Set(out)];
  }
  function dependants(key: string, seen = new Set<string>()): string[] {
    if (seen.has(key)) return [];
    seen.add(key);
    const out: string[] = [];
    for (const d of dependantsOf(items, key)) if (picks[d]) out.push(d, ...dependants(d, seen));
    return [...new Set(out)];
  }
  function toggle(i: MergeItem, on: boolean) {
    const before = { ...picks };
    const next = { ...picks };
    if (on) {
      next[i.key] = { key: i.key, take: 'source' };
      const extra = closure(i.key).filter((k) => byKey.get(k)?.kind !== 'conflict');
      for (const k of extra) next[k] = { key: k, take: 'source' };
      note = extra.length ? { text: `Also took ${extra.map((k) => byKey.get(k)?.label ?? k).join(', ')} — ${i.label} needs ${extra.length === 1 ? 'it' : 'them'}.`, undo: () => ((picks = before), (note = null), schedule()) } : null;
    } else {
      delete next[i.key];
      const deps = dependants(i.key);
      for (const k of deps) delete next[k];
      note = deps.length ? { text: `Also left out ${deps.map((k) => byKey.get(k)?.label ?? k).join(', ')} — ${deps.length === 1 ? 'it needs' : 'they need'} ${i.label}.`, undo: () => ((picks = before), (note = null), schedule()) } : null;
    }
    picks = next;
    schedule();
  }
  function decide(i: MergeItem, take: 'target' | 'source' | 'custom') {
    const cur = picks[i.key];
    picks = { ...picks, [i.key]: take === 'custom' ? { key: i.key, take, value: cur?.take === 'custom' ? cur.value : (i.target.value ?? '') } : { key: i.key, take } };
    schedule();
  }
  function setCustom(i: MergeItem, raw: string) {
    const t = typeof (i.target.has ? i.target.value : i.source.value);
    const value: unknown = t === 'number' && raw.trim() !== '' && Number.isFinite(Number(raw)) ? Number(raw) : t === 'boolean' && (raw === 'true' || raw === 'false') ? raw === 'true' : raw;
    picks = { ...picks, [i.key]: { key: i.key, take: 'custom', value } };
    schedule();
  }
  function selectAll(list: MergeChoice[]) {
    const next = { ...picks };
    for (const c of list) next[c.key] = c;
    for (const c of list) for (const k of closure(c.key)) if (byKey.get(k)?.kind !== 'conflict') next[k] = { key: k, take: 'source' };
    picks = next;
    note = null;
    schedule();
  }
  function clearAll() {
    picks = {};
    note = null;
    schedule();
  }

  // ── preview (debounced)
  let timer: ReturnType<typeof setTimeout> | null = null;
  let seq = 0;
  function coercedValues(): Record<string, unknown> | undefined {
    const miss = preview?.missing.variables ?? [];
    const out: Record<string, unknown> = {};
    for (const [k, v] of Object.entries(values)) {
      if (v === '') continue;
      const t = miss.find((m) => m.variableSlug === k)?.type;
      out[k] = t === 'number' && Number.isFinite(Number(v)) ? Number(v) : t === 'boolean' ? v === 'true' : t === 'json' || t === 'list' ? safeJson(v) : v;
    }
    return Object.keys(out).length ? out : undefined;
  }
  const safeJson = (s: string) => {
    try {
      return JSON.parse(s);
    } catch {
      return s;
    }
  };
  function req() {
    return {
      source: plan!.source.point,
      target: plan!.target.branch,
      targetHead: plan!.target.head?.commitId ?? null,
      sourceCommit: plan!.source.commit?.commitId ?? null,
      choices,
      values: coercedValues() as Record<string, never> | undefined,
      ...(host.scope === 'deployment' && Object.values(secretInputs).some((v) => v !== '') ? { secretValues: Object.fromEntries(Object.entries(secretInputs).filter(([, v]) => v !== '')) } : {}),
    };
  }
  function schedule() {
    if (ro || !plan) return;
    if (timer) clearTimeout(timer);
    timer = setTimeout(runPreview, 350);
  }
  async function runPreview() {
    if (!plan || ro) return;
    const my = ++seq;
    previewBusy = true;
    previewError = null;
    try {
      const p = await host.preview(req());
      if (my === seq) {
        preview = p;
        for (const v of p.missing.variables) if (!askVars.some((x) => x.variableSlug === v.variableSlug)) askVars.push(v);
        if (host.scope === 'deployment') for (const x of p.missing.secrets) if (!askSecrets.some((y) => y.secretSlug === x.secretSlug)) askSecrets.push(x);
      }
    } catch (e) {
      if (my === seq) previewError = e instanceof Error ? e.message : String(e);
    } finally {
      if (my === seq) previewBusy = false;
    }
  }
  $effect(() => {
    if (plan && !ro && !preview && !previewBusy && planId) schedule();
  });

  async function doMerge(message: string) {
    merging = true;
    try {
      const r = await host.merge({ ...req(), message });
      dialogOpen = false;
      host.changed?.(r);
      onmerged?.(r);
    } finally {
      merging = false;
    }
  }

  async function loadBlobs(i: MergeItem) {
    if (!i.blob || !host.blobText) return;
    for (const sha of [i.blob.base, i.blob.target, i.blob.source]) {
      if (!sha || sha in blobs) continue;
      blobs[sha] = undefined;
      host.blobText(sha).then(
        (t) => (blobs[sha] = t),
        () => (blobs[sha] = null),
      );
    }
  }
  function expand(i: MergeItem) {
    expanded[i.key] = !expanded[i.key];
    if (expanded[i.key]) void loadBlobs(i);
  }
  const errCount = $derived(preview?.validation.errors.length ?? 0);
  /** fields still empty (the required-value errors they cause are shown as these fields, not as errors) */
  const needVars = $derived((preview?.missing.variables ?? []).map((v) => v.variableSlug));
  const needSecrets = $derived(host.scope === 'deployment' ? (preview?.missing.secrets ?? []).map((v) => v.secretSlug) : []);
  const isNeedIssue = (i: { code: string; path: string }) =>
    (i.code === 'V_SECRET_REQUIRED' && needSecrets.some((s) => i.path === `secrets[${s}].value`)) || (i.code === 'V_VAR_REQUIRED' && needVars.some((s) => i.path.startsWith(`variables[${s}]`)));
  const shownIssues = $derived(preview ? [...preview.validation.errors, ...preview.validation.warnings].filter((i) => !isNeedIssue(i)) : []);
  const otherErrCount = $derived(preview ? preview.validation.errors.filter((i) => !isNeedIssue(i)).length : 0);
  const needCount = $derived(needVars.length + needSecrets.length);
  const canMerge = $derived(!ro && host.canWrite && !!plan && plan.target.workingClean && takenItems.length > 0 && unresolved === 0 && !previewBusy && !!preview && errCount === 0 && (preview?.choiceErrors.length ?? 0) === 0);
  const mergeBlocked = $derived(!plan ? 'Loading…' : !plan.target.workingClean ? `Commit or discard the ${plan.target.uncommitted} ${host.scope === 'agent' ? 'change' + (plan.target.uncommitted === 1 ? '' : 's') + ' made on the server' : 'uncommitted change' + (plan.target.uncommitted === 1 ? '' : 's') + ' of ' + plan.target.label} first` : unresolved ? `${unresolved} conflict${unresolved === 1 ? '' : 's'} need a decision` : !takenItems.length ? 'Pick at least one change' : needCount && !otherErrCount ? `Fill in ${needCount === 1 ? 'the value' : `the ${needCount} values`} below to merge` : errCount ? 'Fix the problems below to merge' : null);
  /** one-line summary of a dev-tool preset (the old Diff showed it): "Plant database — postgres · host · user · password: secret" */
  function devToolSummary(it: MergeItem): string | null {
    const v = (it.source.has ? it.source.value : it.target.value) as Record<string, unknown> | null;
    if (!v || typeof v !== 'object') return null;
    const conn = (v.connection ?? v) as Record<string, unknown>;
    const auth = (v.auth ?? {}) as Record<string, { kind?: string } | null>;
    const parts = [v.kind, conn.host ?? conn.url ?? conn.containerSlug, conn.database, (auth.user as unknown as string) ?? conn.user].filter((x) => typeof x === 'string' && x) as string[];
    const pw = auth.password?.kind ?? auth.token?.kind;
    if (pw) parts.push(`credential: ${pw === 'literal' ? 'value (hidden)' : pw}`);
    return `${(v.label as string) || (v.presetSlug as string) || ''}${parts.length ? ' — ' + parts.join(' · ') : ''}` || null;
  }
</script>

{#snippet slotView(label: string, s: { has: boolean; value: unknown } | null, tone: string)}
  <div class="slot {tone}">
    <div class="tiny strong">{label}</div>
    {#if s === null}<div class="tiny muted">no common base</div>{:else if !s.has}<div class="tiny muted">absent</div>{:else}<pre class="val">{show(s.value, s.has)}</pre>{/if}
  </div>
{/snippet}

{#snippet row(i: MergeItem)}
  {@const p = picks[i.key]}
  <li class="item" class:taken={!!p && p.take !== 'target'} data-testid="merge-item-{i.key}" data-kind={i.kind} data-effect={i.effect}>
    <div class="line">
      {#if !ro && i.kind !== 'conflict'}
        <input type="checkbox" class="cb" checked={!!p && p.take === 'source'} disabled={!!i.locked || !host.canWrite} onchange={(e) => toggle(i, (e.currentTarget as HTMLInputElement).checked)} aria-label="Take {i.label}" data-testid="merge-take-{i.key}" />
      {/if}
      <button type="button" class="lbl" onclick={() => expand(i)} aria-expanded={!!expanded[i.key]} data-testid="merge-expand-{i.key}">
        <Icon name={expanded[i.key] ? 'chevronDown' : 'chevronRight'} size={13} /><span class="ellipsis">{i.label}</span>
      </button>
      {#if !ro && i.effect === 'remove' && (i.kind === 'ours' || i.kind === 'remove')}
        <span class="badge {KIND_TONE[i.kind]}" data-kindlabel="target-only">Only in {plan?.target.label ?? 'target'}</span>
        <span class="tiny muted">kept unless ticked</span>
      {:else}
        <span class="badge {KIND_TONE[i.kind]}">{KIND_LABEL[i.kind]}</span>
        <span class="tiny muted">{EFFECT_LABEL[i.effect]}</span>
      {/if}
      {#if i.locked}<span class="badge outline" title={i.locked}><Icon name="lock" size={11} /> locked</span>{/if}
      {#each i.requires as r (r)}<span class="chip tiny" title="needs {r}">needs {byKey.get(r)?.label ?? r}</span>{/each}
    </div>
    {#if i.locked}<div class="tiny muted ind">{i.locked}</div>{/if}
    {#if i.section === 'devTools'}{@const dt = devToolSummary(i)}{#if dt}<div class="tiny muted ind" data-testid="merge-devtool-{i.key}">{dt}</div>{/if}{/if}
    {#if i.effect === 'remove' && !ro}<div class="tiny warn-text ind">{removalText(i, host.scope)}</div>{/if}
    {#if i.note && !(i.effect === 'remove' && !ro)}<div class="tiny muted ind">{i.note}</div>{/if}
    {#if i.kind === 'conflict' && !ro}
      <div class="ind row gap-s conflict">
        <SegmentedControl size="sm" ariaLabel="Resolve {i.label}" value={p?.take} options={[{ value: 'target', label: 'Keep target' }, { value: 'source', label: 'Take source' }, ...(i.scalar ? [{ value: 'custom' as const, label: 'Custom' }] : [])]} onchange={(v) => decide(i, v as 'target' | 'source' | 'custom')} disabled={!!i.locked || !host.canWrite} testid="merge-resolve-{i.key}" />
        {#if p?.take === 'custom'}<input class="input sm mono" value={p.value === null ? '' : String(p.value ?? '')} oninput={(e) => setCustom(i, (e.currentTarget as HTMLInputElement).value)} aria-label="Custom value" data-testid="merge-custom-{i.key}" />{/if}
      </div>
    {/if}
    {#if expanded[i.key]}
      <div class="sides" data-testid="merge-sides-{i.key}">
        {@render slotView('Base', i.base, 'base')}
        {@render slotView(`Target · ${plan?.target.label ?? ''}`, i.target, 'tgt')}
        {@render slotView(`Source · ${plan?.source.label ?? ''}`, i.source, 'src')}
      </div>
      {#if i.blob && (i.blob.target || i.blob.source)}
        {@const a = i.blob.target ? blobs[i.blob.target] : ''}
        {@const b = i.blob.source ? blobs[i.blob.source] : ''}
        {#if a === undefined || b === undefined}<p class="tiny muted ind">Loading content…</p>
        {:else if a === null || b === null}<p class="tiny muted ind">Binary or large content — compare by sha256.</p>
        {:else}<div class="ind"><TextDiff before={a} after={b} testid="merge-textdiff-{i.key}" /></div>{/if}
      {/if}
    {/if}
  </li>
{/snippet}

<div class="mv" data-testid={testid}>
  {#if loading && !plan}
    <div class="card card-pad muted">Comparing…</div>
  {:else if loadError}
    <div class="banner bad"><Icon name="alert" /><div class="grow">{loadError}</div><Btn size="sm" onclick={load}>Retry</Btn></div>
  {:else if plan}
    <div class="card card-pad head">
      <div class="row gap-s wrap">
        {#if sourcePicker}{@render sourcePicker()}{:else}<span class="badge accent" data-testid="merge-source">{plan.source.label}</span>{/if}
        <Icon name="arrowRight" />
        <span class="badge outline" data-testid="merge-target">{plan.target.label}</span>
        {#if request}<Btn size="xs" variant="ghost" icon="refresh" onclick={() => ((plan = null), load())} data-testid="merge-refresh">Refresh</Btn>{/if}
      </div>
      <p class="small muted m0" data-testid="merge-base">
        {#if plan.base.kind === 'none'}No common history — every difference is shown; conflicts cannot be told apart.{:else}Base: {plan.base.label}{/if}
        {#if plan.fastForward && !ro} · <strong>fast-forward</strong>: the target has no own changes{/if}
      </p>
      {#if plan.references?.updatedSites.length}<p class="tiny muted m0">References to other sites are refreshed from the source: {plan.references.updatedSites.join(', ')}</p>{/if}
      {#if !ro && pendingNote}<div class="banner info small" data-testid="merge-pending"><Icon name="info" /><div class="grow">{pendingNote}</div></div>{/if}
      {#if !ro && !plan.target.workingClean}<div class="banner warn small" data-testid="merge-dirty"><Icon name="alert" /><div class="grow">{#if host.scope === 'agent'}{plan.target.uncommitted} change{plan.target.uncommitted === 1 ? '' : 's'} made on the server (outside the agent) {plan.target.uncommitted === 1 ? 'is' : 'are'} not committed — commit or discard them before a merge.{:else}{plan.target.label} has {plan.target.uncommitted} uncommitted change{plan.target.uncommitted === 1 ? '' : 's'} — commit or discard them before a merge.{/if}</div></div>{/if}
      <div class="row gap-s wrap chips" role="group" aria-label="Filter">
        {#each FILTERS as f (f.value)}
          <button type="button" class="chip" class:on={filter === f.value} aria-pressed={filter === f.value} onclick={() => (filter = f.value)} data-testid="merge-filter-{f.value}">{f.label} <span class="muted">{counts[f.value]}</span></button>
        {/each}
      </div>
      {#if !ro && host.canWrite}
        <div class="row gap-s wrap">
          <Btn size="xs" onclick={() => selectAll(cleanSourceChoices(items).filter((c) => byKey.get(c.key)?.effect !== 'remove'))} data-testid="merge-select-source">Select all source changes</Btn>
          <Btn size="xs" onclick={() => selectAll(removalChoices(items))} disabled={!removals.length} data-testid="merge-select-removals">Select all removals</Btn>
          <Btn size="xs" variant="ghost" onclick={clearAll} data-testid="merge-clear">Clear</Btn>
          <span class="tiny muted">{takenItems.length} of {items.length} picked</span>
        </div>
      {/if}
      {#if note}<div class="banner info small" data-testid="merge-note"><Icon name="info" /><div class="grow">{note.text}</div><Btn size="xs" variant="ghost" onclick={note.undo} data-testid="merge-note-undo">Undo</Btn></div>{/if}
    </div>

    {#if !items.length}
      <div class="card card-pad muted" data-testid="merge-empty">No differences — {plan.source.label} and {plan.target.label} are the same here.</div>
    {/if}
    {#each groups as g (g.id)}
      <section class="card grp" data-testid="merge-group-{g.id}">
        <div class="card-head"><h4>{g.title}</h4><span class="tiny muted">{g.items.length}</span></div>
        <ul class="items">{#each g.items as i (i.key)}{@render row(i)}{/each}</ul>
      </section>
    {/each}
    {#if showRemovals.length}
      <section class="card grp removed" data-testid="merge-group-removed">
        <div class="card-head"><h4><Icon name="trash" size={14} /> Removed by this merge</h4><span class="tiny muted">{showRemovals.length} — only what you tick is removed</span></div>
        <ul class="items">{#each showRemovals as i (i.key)}{@render row(i)}{/each}</ul>
      </section>
    {/if}

    {#if !ro}
      <div class="pv" class:open={sheet} data-testid="merge-preview">
        <div class="row between pvh">
          <button type="button" class="linkish small" onclick={() => (sheet = !sheet)} aria-expanded={sheet} data-testid="merge-preview-toggle">
            <Icon name={sheet ? 'chevronDown' : 'arrowUp'} size={13} />
            {#if previewBusy}Checking…{:else if previewError}Preview failed{:else if preview}{preview.taken} change{preview.taken === 1 ? '' : 's'} picked{#if needCount} · {needCount} value{needCount === 1 ? '' : 's'} needed{/if}{#if otherErrCount} · {otherErrCount} problem{otherErrCount === 1 ? '' : 's'}{/if}{#if preview.validation.warnings.length} · {preview.validation.warnings.length} warning{preview.validation.warnings.length === 1 ? '' : 's'}{/if}{#if unresolved} · {unresolved} unresolved{/if}{:else}Preview{/if}
          </button>
          <Btn variant="primary" size="sm" disabled={!canMerge} title={mergeBlocked ?? undefined} onclick={() => (dialogOpen = true)} data-testid="merge-submit">{mergeLabel ?? (host.scope === 'agent' ? 'Merge & apply…' : 'Merge…')}</Btn>
        </div>
        <div class="pvb">
          {#if mergeBlocked && !previewBusy}<p class="tiny muted m0">{mergeBlocked}</p>{/if}
          {#if previewError}<div class="banner bad small"><Icon name="alert" /><div class="grow">{previewError}</div></div>{/if}
          {#if preview}
            {#each preview.choiceErrors as e (e.key)}<div class="banner bad small"><div class="grow">{e.key}: {e.message}</div></div>{/each}
            {#if askVars.length || askSecrets.length}
              <div class="stack tight" data-testid="merge-missing">
                <div class="tiny strong">Values needed</div>
                <p class="tiny muted m0">This merge adds settings that must have a value before the deployment can use them.</p>
                {#each askVars as v (v.variableSlug)}
                  <label class="row gap-s small"><span class="mono">{v.variableSlug}</span><input class="input sm" placeholder={v.type} bind:value={values[v.variableSlug]} oninput={schedule} data-testid="merge-value-{v.variableSlug}" /></label>
                {/each}
                {#each askSecrets as x (x.secretSlug)}
                  <label class="row gap-s small"><span class="mono">{x.secretSlug}</span><input class="input sm" type="password" autocomplete="off" placeholder="secret value" bind:value={secretInputs[x.secretSlug]} oninput={schedule} data-testid="merge-secret-{x.secretSlug}" /></label>
                {/each}
              </div>
            {/if}
            {#if shownIssues.length}<Issues issues={shownIssues} title="Result" testid="merge-issues" />{/if}
            {#if host.scope === 'agent' && preview.missing.secrets.length}<p class="tiny warn-text m0">These secrets have no value yet — set them on the deployment's Secrets page in the cloud: {preview.missing.secrets.map((s) => s.secretSlug).join(', ')}</p>{/if}
            {#each preview.warnings as w, k (k)}<p class="tiny warn-text m0">{w}</p>{/each}
          {/if}
        </div>
      </div>
      <CommitDialog
        bind:open={dialogOpen}
        title={host.scope === 'agent' ? 'Merge into this site' : `Merge into ${plan.target.label}`}
        primaryLabel={host.scope === 'agent' ? 'Merge & apply' : 'Merge'}
        intro="{preview?.kind === 'cherry-pick' ? 'Cherry-pick' : 'Merge'} {takenItems.length} change{takenItems.length === 1 ? '' : 's'} from {plan.source.label} into {plan.target.label}."
        items={takenItems}
        issues={shownIssues}
        warnings={[...warnings]}
        runtime={preview?.runtime ?? []}
        suggestPrefix="{preview?.kind === 'cherry-pick' ? 'Cherry-pick' : 'Merge'} from {plan.source.label}: "
        author={host.actor()}
        busy={merging}
        onsubmit={doMerge}
        testid="merge-commit-dialog" />
    {/if}
  {/if}
</div>

<style>
  .mv {
    display: flex;
    flex-direction: column;
    gap: 12px;
    /* room under the sticky Preview bar so the last rows and the page end stay reachable */
    padding-bottom: 32px;
  }
  .head {
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  .wrap {
    flex-wrap: wrap;
  }
  .m0 {
    margin: 0;
  }
  .chip.on {
    border-color: var(--accent);
    color: var(--accent-text);
    background: var(--accent-soft);
  }
  .chip {
    cursor: pointer;
  }
  .items {
    list-style: none;
    margin: 0;
    padding: 0;
  }
  .item {
    border-top: 1px solid var(--border);
    padding: 7px 12px;
  }
  .item.taken {
    background: color-mix(in srgb, var(--accent-soft) 45%, transparent);
  }
  .line {
    display: flex;
    gap: 8px;
    align-items: center;
    flex-wrap: wrap;
    min-width: 0;
  }
  .cb {
    width: 16px;
    height: 16px;
    accent-color: var(--accent);
  }
  .lbl {
    background: none;
    border: 0;
    padding: 0;
    color: var(--text);
    display: inline-flex;
    align-items: center;
    gap: 4px;
    cursor: pointer;
    min-width: 0;
    max-width: 100%;
    font: inherit;
  }
  .ind {
    margin: 4px 0 0 24px;
  }
  .sides {
    display: grid;
    grid-template-columns: repeat(3, minmax(0, 1fr));
    gap: 8px;
    margin: 8px 0 0 24px;
  }
  .slot {
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    padding: 6px 8px;
    min-width: 0;
  }
  .slot.src {
    border-color: color-mix(in srgb, var(--green) 40%, var(--border));
  }
  .slot.tgt {
    border-color: color-mix(in srgb, var(--blue) 35%, var(--border));
  }
  .val {
    margin: 4px 0 0;
    font-family: var(--font-mono);
    font-size: 12px;
    white-space: pre-wrap;
    word-break: break-word;
    max-height: 240px;
    overflow: auto;
  }
  .removed {
    border-color: color-mix(in srgb, var(--red) 35%, var(--border));
  }
  .pv {
    position: sticky;
    bottom: 12px;
    z-index: var(--z-editbar, var(--z-sticky));
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: 12px;
    box-shadow: var(--shadow-md);
    padding: 10px 12px;
  }
  .pvb {
    display: flex;
    flex-direction: column;
    gap: 6px;
    margin-top: 6px;
    max-height: 40vh;
    overflow: auto;
  }
  .linkish {
    background: none;
    border: 0;
    padding: 0;
    color: var(--accent-text);
    cursor: pointer;
    display: inline-flex;
    gap: 4px;
    align-items: center;
    font: inherit;
  }
  @media (max-width: 600px) {
    .sides {
      grid-template-columns: 1fr;
      margin-left: 0;
    }
    .ind {
      margin-left: 0;
    }
    .mv {
      padding-bottom: 72px;
    }
    .pv {
      position: sticky;
      bottom: 0;
      border-radius: 12px 12px 0 0;
    }
    .pv:not(.open) .pvb {
      display: none;
    }
    .pv.open .pvb {
      max-height: 55vh;
    }
  }
</style>
