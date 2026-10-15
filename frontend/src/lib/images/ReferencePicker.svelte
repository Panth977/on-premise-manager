<script lang="ts" module>
  /** Where the typed reference text is: choosing a host, a repository on that host, or a tag of that repository. */
  export type RefStage = { kind: 'host'; q: string } | { kind: 'repo'; host: string; q: string } | { kind: 'tag'; host: string; repo: string; q: string };
  export function refStage(t: string, hostOf: (h: string) => string): RefStage {
    const slash = t.indexOf('/');
    if (slash < 0) return { kind: 'host', q: t };
    const first = t.slice(0, slash);
    const isHost = first.includes('.') || first.includes(':') || first === 'localhost';
    const host = isHost ? hostOf(first) : 'docker.io';
    const rest = isHost ? t.slice(slash + 1) : t;
    const at = rest.indexOf('@');
    const body = at >= 0 ? rest.slice(0, at) : rest;
    const colon = body.lastIndexOf(':');
    if (colon >= 0) return { kind: 'tag', host, repo: body.slice(0, colon), q: body.slice(colon + 1) };
    return { kind: 'repo', host, q: body };
  }
</script>

<script lang="ts">
  /**
   * Docker image reference picker: a free-text combobox (pasting a full reference works) with staged autocomplete
   * registry host → repositories (listRegistryRepositories) → tags newest first (listRegistryTags). Shows the parsed
   * parts (host / repository / tag / digest) and the registry whose credentials are used: auto-matched by host
   * (matchRegistries; several → dropdown; none → public), changeable (then `registryAuto` = false).
   */
  import type { Image, RegistryView } from '@opm/shared';
  import { matchRegistries, parseImageReference, registryHost } from '@opm/shared';
  import { api, errMsg } from '../api';
  import { relTime, bytes, shortSha } from '../util/format';
  import Combobox, { type ComboOption } from '../ui/Combobox.svelte';
  import Select, { type SelectGroup } from '../ui/Select.svelte';

  type Props = {
    value: string;
    registryId: string | null;
    /** true = the registry follows the reference host */
    registryAuto?: boolean;
    registries: RegistryView[];
    /** images of this service: their references are marked "already registered" */
    registered?: Image[];
    id?: string;
    testid?: string;
    invalid?: boolean;
    disabled?: boolean;
    inputEl?: HTMLInputElement | null;
    onchange?: () => void;
  };
  let { value = $bindable(''), registryId = $bindable(null), registryAuto = $bindable(true), registries, registered = [], id, testid = 'ri-ref', invalid = false, disabled = false, inputEl = $bindable(null), onchange }: Props = $props();

  const hostOf = (h: string) => registryHost(`https://${h}`);
  const parsed = $derived(value.trim() ? parseImageReference(value) : null);
  const host = $derived(parsed?.ok ? parsed.parts.host : null);
  const matches = $derived(host ? matchRegistries(host, registries) : []);
  const others = $derived(registries.filter((r) => !matches.includes(r)));
  const regGroups = $derived<SelectGroup[]>([
    ...(matches.length ? [{ label: `Registries for ${host}`, options: matches.map((r) => ({ value: r.registryId, label: r.label, sub: r.username ? r.username : 'anonymous' })) }] : []),
    { options: [{ value: '', label: 'public (no credentials)' }] },
    ...(others.length ? [{ label: host ? 'Other registries (host differs)' : 'Registries', options: others.map((r) => ({ value: r.registryId, label: r.label, sub: registryHost(r.url) })) }] : []),
  ]);
  const regOf = (rid: string | null) => registries.find((r) => r.registryId === rid) ?? null;

  // auto-match the registry when the host changes
  $effect(() => {
    const h = host;
    if (!registryAuto || h === null) return;
    const m = matchRegistries(h, registries);
    const next = m.some((r) => r.registryId === registryId) ? registryId : (m[0]?.registryId ?? null);
    if (next !== registryId) registryId = next;
  });

  const taken = $derived(new Map(registered.map((i) => [`${i.source.host}/${i.source.repository}:${i.source.tag}`, i.version])));
  let message = $state<string | null>(null);

  function listRegistryFor(h: string): RegistryView | null {
    const sel = regOf(registryId);
    if (sel && registryHost(sel.url) === h) return sel;
    return matchRegistries(h, registries)[0] ?? null;
  }
  let combo = $state<{ openWith: (t?: string) => void } | null>(null);
  /** open the list (after a clone: the tags of the copied repository) */
  export function openList() {
    combo?.openWith();
  }
  async function load(t: string, typed: boolean): Promise<ComboOption[]> {
    message = null;
    const st0 = refStage(t, hostOf);
    // just opened on a complete reference → list every tag of that repository
    const st = !typed && st0.kind === 'tag' ? { ...st0, q: '' } : st0;
    if (st.kind === 'host') {
      const hosts = new Map<string, string[]>();
      for (const r of registries) {
        const h = registryHost(r.url);
        hosts.set(h, [...(hosts.get(h) ?? []), r.label]);
      }
      if (!hosts.size) message = 'No registry yet — paste a full public reference, e.g. docker.io/library/redis:7.4';
      const q = st.q.toLowerCase();
      return [...hosts]
        .filter(([h, labels]) => !q || h.includes(q) || labels.join(' ').toLowerCase().includes(q))
        .map(([h, labels]) => ({ value: `${h}/`, label: `${h}/`, sub: labels.join(' · '), icon: 'lucide:container' }));
    }
    const reg = listRegistryFor(st.host);
    if (!reg) {
      message = `No registry for ${st.host} — type the full reference (public images need no registry).`;
      return [];
    }
    if (st.kind === 'repo') {
      const r = await api.listRegistryRepositories({ registryId: reg.registryId, query: st.q ? { q: st.q } : {} });
      if (!r.supported || r.message) message = r.message ?? 'This registry cannot list repositories — type the name.';
      return r.items.map((x) => ({ value: `${st.host}/${x.name}:`, label: x.name, sub: x.description ?? undefined, badge: x.official ? 'official' : undefined, detail: x.updatedAt ? relTime(x.updatedAt) : undefined }));
    }
    try {
      const r = await api.listRegistryTags({ registryId: reg.registryId, query: { repository: st.repo, ...(st.q ? { q: st.q } : {}) } });
      if (!r.supported || r.message) message = r.message ?? 'This registry cannot list tags — type the tag.';
      return r.items.map((x) => {
        const ref = `${st.host}/${st.repo}:${x.tag}`;
        const v = taken.get(ref);
        return { value: ref, label: x.tag, sub: [x.digest ? shortSha(x.digest) : null, x.sizeBytes ? bytes(x.sizeBytes) : null].filter(Boolean).join(' · ') || undefined, detail: x.pushedAt ? relTime(x.pushedAt) : undefined, badge: v ? `already registered (${v})` : undefined, disabled: !!v };
      });
    } catch (e) {
      message = errMsg(e);
      return [];
    }
  }
  function onselect(o: ComboOption): boolean {
    onchange?.();
    // a host or a repository was picked → continue to the next stage
    return o.value.endsWith('/') || o.value.endsWith(':');
  }
  function pickRegistry(v: string) {
    registryAuto = false;
    registryId = v || null;
    onchange?.();
  }
</script>

<div class="rp">
  <Combobox
    bind:this={combo}
    bind:value
    bind:inputEl
    {load}
    loadKey={registryId}
    loadAll
    freeText
    mono
    clearable
    {id}
    {testid}
    {invalid}
    {disabled}
    {message}
    emptyText="Type or paste a reference"
    placeholder="docker.io/library/redis:7.4.1"
    ariaLabel="Image reference"
    {onselect}
    oninput={() => onchange?.()}
  />
  {#if parsed?.ok}
    {@const p = parsed.parts}
    <div class="parts" data-testid="ri-ref-parts">
      <span class="pc"><span class="k">host</span>{p.host}</span>
      <span class="pc"><span class="k">repository</span>{p.repository}</span>
      {#if p.tag}<span class="pc"><span class="k">tag</span>{p.tag}</span>{/if}
      {#if p.digest}<span class="pc"><span class="k">digest</span>{shortSha(p.digest)}</span>{/if}
    </div>
  {/if}
  <div class="reg">
    <label class="small muted" for="{id}-reg">Credentials</label>
    <div class="rsel"><Select id="{id}-reg" value={registryId ?? ''} groups={regGroups} size="sm" {disabled} ariaLabel="Credentials" testid="ri-registry" minWidth={260} onchange={pickRegistry} /></div>
    <span class="xs faint">
      {#if registryAuto}{matches.length > 1 ? `${matches.length} registries match ${host} — pick one` : matches.length ? 'matched by host' : host ? `no registry for ${host}: pulled without credentials` : 'matched by the reference host'}{:else}chosen by you{/if}
    </span>
  </div>
</div>

<style>
  .rp {
    display: flex;
    flex-direction: column;
    gap: 6px;
    min-width: 0;
  }
  .parts {
    display: flex;
    flex-wrap: wrap;
    gap: 4px;
  }
  .pc {
    display: inline-flex;
    align-items: center;
    gap: 5px;
    font-family: var(--font-mono);
    font-size: var(--fs-xs);
    background: var(--surface-2);
    border: 1px solid var(--border);
    border-radius: 999px;
    padding: 1px 8px 1px 3px;
    max-width: 100%;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .k {
    font-family: var(--font-sans);
    background: var(--accent-soft);
    color: var(--accent-text);
    border-radius: 999px;
    padding: 0 6px;
  }
  .reg {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
  }
  .rsel {
    width: 260px;
    max-width: 100%;
  }
</style>
