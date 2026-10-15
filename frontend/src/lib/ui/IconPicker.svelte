<script lang="ts">
  /**
   * Icon picker (services, templates, tags, deployments, account avatars).
   * Trigger = button with a preview. Opens a modal with two tabs:
   *  - Icons: the curated Lucide set (shared/icons.ts), searchable, grouped; roving focus (arrows / Home / End,
   *    Enter picks, Esc closes).
   *  - Uploads: every uploaded icon (listIcons) to reuse; Upload button + drag-drop (png / svg / webp ≤ 64 KiB, checked
   *    here first). An upload is selected right away; a de-duplicated upload (existing: true) selects the existing icon
   *    with the toast "already uploaded — reused". Unused uploads can be deleted (409 → the users are listed).
   * "None" = null = the entity's default icon (DEFAULT_ICONS[type]); for avatars (`initials`) null = initials.
   */
  import type { IconListItem, IconUser } from '@opm/shared';
  import { ICON_CONTENT_TYPES, ICON_MAX_BYTES, parseIcon } from '@opm/shared';
  import { LUCIDE_ICONS, ICON_GROUPS, DEFAULT_ICONS, lucideSvg } from '@opm/shared/icons';
  import { tick } from 'svelte';
  import { api, errMsg, isApiError } from '../api';
  import { toast } from '../toast.svelte';
  import { bytes } from '../util/format';
  import Modal from './Modal.svelte';
  import IconView, { type IconType } from './IconView.svelte';
  import Icon from './Icon.svelte';
  import Spinner from './Spinner.svelte';
  import SegmentedControl from './SegmentedControl.svelte';
  import { ChevronDown, Search, Trash2, Upload, Ban } from './icons';

  type Props = {
    value: string | null;
    type?: IconType;
    /** avatar mode: null shows the initials of this name */
    initials?: string;
    id?: string;
    testid?: string;
    disabled?: boolean;
    invalid?: boolean;
    title?: string;
    onchange?: (v: string | null) => void;
  };
  let { value = $bindable(null), type = 'service', initials, id, testid, disabled = false, invalid = false, title = 'Choose an icon', onchange }: Props = $props();

  let open = $state(false);
  let tab = $state<'icons' | 'uploads'>('icons');
  let q = $state('');
  let uploads = $state<IconListItem[] | null>(null);
  let upLoading = $state(false);
  let upError = $state<string | null>(null);
  let uploading = $state(false);
  let dragOver = $state(false);
  let fileEl = $state<HTMLInputElement | null>(null);
  let gridEl = $state<HTMLElement | null>(null);
  let active = $state(0);
  let delUsers = $state<{ id: string; users: IconUser[] } | null>(null);

  const parsed = $derived(parseIcon(value));
  const uploadName = $derived(parsed?.kind === 'upload' ? (uploads?.find((u) => u.iconId === parsed.id)?.name ?? 'uploaded icon') : null);
  const caption = $derived(!value ? (initials ? 'Initials (none)' : `Default (${DEFAULT_ICONS[type]})`) : parsed?.kind === 'lucide' ? parsed.name : parsed?.kind === 'upload' ? uploadName : 'image URL');

  const ql = $derived(q.trim().toLowerCase());
  const matches = $derived(ql ? LUCIDE_ICONS.filter((i) => i.name.includes(ql) || i.group.includes(ql)) : LUCIDE_ICONS);
  const groups = $derived(ql ? [{ group: 'results', items: matches }] : ICON_GROUPS.map((g) => ({ group: g as string, items: LUCIDE_ICONS.filter((i) => i.group === g) })).filter((g) => g.items.length));
  const flat = $derived(groups.flatMap((g) => g.items.map((i) => i.name)));
  const selectedName = $derived(parsed?.kind === 'lucide' ? parsed.name : null);

  async function show() {
    if (disabled) return;
    q = '';
    tab = parsed?.kind === 'upload' ? 'uploads' : 'icons';
    delUsers = null;
    open = true;
    loadUploads();
    await tick();
    const i = selectedName ? flat.indexOf(selectedName) : -1;
    active = Math.max(0, i);
    if (i >= 0) gridEl?.querySelector<HTMLElement>(`[data-name="${selectedName}"]`)?.scrollIntoView({ block: 'center' });
  }
  async function loadUploads() {
    upLoading = true;
    upError = null;
    try {
      uploads = (await api.listIcons({})).items;
    } catch (e) {
      upError = errMsg(e);
    } finally {
      upLoading = false;
    }
  }
  function choose(v: string | null) {
    value = v;
    onchange?.(v);
    open = false;
  }

  // roving focus over the Lucide grid
  function cols(): number {
    const btns = gridEl?.querySelectorAll<HTMLElement>('.ic');
    if (!btns || btns.length < 2) return 1;
    const top = btns[0]!.offsetTop;
    let n = 0;
    for (const b of btns) {
      if (b.offsetTop !== top) break;
      n++;
    }
    return Math.max(1, n);
  }
  function focusActive() {
    const name = flat[active];
    if (!name) return;
    const el = gridEl?.querySelector<HTMLElement>(`[data-name="${name}"]`);
    el?.focus();
    el?.scrollIntoView({ block: 'nearest' });
  }
  function gridKey(e: KeyboardEvent) {
    const n = flat.length;
    if (!n) return;
    const c = cols();
    let next = active;
    if (e.key === 'ArrowRight') next = Math.min(n - 1, active + 1);
    else if (e.key === 'ArrowLeft') next = Math.max(0, active - 1);
    else if (e.key === 'ArrowDown') next = Math.min(n - 1, active + c);
    else if (e.key === 'ArrowUp') next = Math.max(0, active - c);
    else if (e.key === 'Home') next = 0;
    else if (e.key === 'End') next = n - 1;
    else if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      choose(`lucide:${flat[active]}`);
      return;
    } else return;
    e.preventDefault();
    active = next;
    focusActive();
  }
  function searchKey(e: KeyboardEvent) {
    if (e.key === 'ArrowDown' || e.key === 'Enter') {
      e.preventDefault();
      if (e.key === 'Enter' && flat.length) return choose(`lucide:${flat[0]}`);
      active = 0;
      focusActive();
    }
  }
  $effect(() => {
    void ql;
    active = 0;
  });

  // uploads
  const accept = ICON_CONTENT_TYPES.join(',');
  async function upload(file: File) {
    upError = null;
    const type = file.type || (file.name.endsWith('.svg') ? 'image/svg+xml' : '');
    if (!(ICON_CONTENT_TYPES as readonly string[]).includes(type)) {
      upError = `“${file.name}” is ${type || 'an unknown type'} — use PNG, SVG or WebP.`;
      return;
    }
    if (file.size > ICON_MAX_BYTES) {
      upError = `“${file.name}” is ${bytes(file.size)} — icons must be at most ${bytes(ICON_MAX_BYTES)}.`;
      return;
    }
    if (!file.size) {
      upError = `“${file.name}” is empty.`;
      return;
    }
    uploading = true;
    try {
      const name = file.name.replace(/\.[a-z0-9]+$/i, '').slice(0, 80) || 'icon';
      const r = await api.uploadIcon({ body: file, contentType: type, query: { name } });
      await loadUploads();
      if (r.existing) toast.info('Icon already uploaded — reused', r.icon.name);
      else toast.success('Icon uploaded', r.icon.name);
      choose(`upload:${r.icon.iconId}`);
    } catch (e) {
      upError = errMsg(e);
    } finally {
      uploading = false;
    }
  }
  function onFiles(list: FileList | null | undefined) {
    const f = list?.[0];
    if (f) upload(f);
    if (fileEl) fileEl.value = '';
  }
  async function del(it: IconListItem) {
    delUsers = null;
    try {
      await api.deleteIcon({ iconId: it.iconId });
      toast.success('Icon deleted', it.name);
      if (value === `upload:${it.iconId}`) {
        value = null;
        onchange?.(null);
      }
      await loadUploads();
    } catch (e) {
      if (isApiError(e) && e.code === 'conflict') {
        delUsers = { id: it.iconId, users: ((e.details as { users?: IconUser[] }).users ?? []) as IconUser[] };
        toast.error(e, 'Icon is in use');
      } else toast.error(e, 'Not deleted');
    }
  }
</script>

<button type="button" class="trig input" class:invalid {id} {disabled} onclick={show} aria-haspopup="dialog" data-testid={testid}>
  <span class="pv" class:round={!!initials}><IconView value={value} {type} size={initials ? 28 : 18} {initials} round={!!initials} /></span>
  <span class="cap" class:faint={!value}>{caption}</span>
  <Icon icon={ChevronDown} size={14} class="chev" />
</button>

<Modal bind:open {title} size="lg" testid="icon-picker">
  <div class="head">
    <SegmentedControl options={[{ value: 'icons', label: 'Icons' }, { value: 'uploads', label: `Uploads${uploads ? ` (${uploads.length})` : ''}` }]} bind:value={tab} ariaLabel="Icon source" testid="ip-tab" />
    <span class="spacer"></span>
    <button type="button" class="btn sm" class:sel={!value} onclick={() => choose(null)} data-testid="ip-none">
      {#if initials}<IconView value={null} initials={initials} size={16} round />{:else}<IconView value={null} {type} size={15} />{/if}
      None ({initials ? 'initials' : 'default icon'})
    </button>
  </div>

  {#if tab === 'icons'}
    <div class="sr">
      <Icon icon={Search} size={14} />
      <input class="sinput" bind:value={q} placeholder="Search {LUCIDE_ICONS.length} icons (e.g. database, user, cloud)" onkeydown={searchKey} aria-label="Search icons" data-autofocus data-testid="ip-search" />
    </div>
    <div class="grid-wrap" bind:this={gridEl} role="listbox" aria-label="Icons" tabindex="-1" onkeydown={gridKey}>
      {#each groups as g (g.group)}
        {#if !ql}<div class="gh">{g.group}</div>{/if}
        <div class="grid">
          {#each g.items as it (it.name)}
            {@const idx = flat.indexOf(it.name)}
            <button
              type="button"
              role="option"
              aria-selected={selectedName === it.name}
              class="ic"
              class:sel={selectedName === it.name}
              tabindex={idx === active ? 0 : -1}
              title={it.name}
              aria-label={it.name}
              data-name={it.name}
              onfocus={() => (active = idx)}
              onclick={() => choose(`lucide:${it.name}`)}
              data-testid="ip-icon">{@html lucideSvg(it.name, 20, 1.75)}</button
            >
          {/each}
        </div>
      {:else}
        <p class="none">No icon matches “{q}”. Try another word, or upload your own in the Uploads tab.</p>
      {/each}
    </div>
  {:else}
    <div
      class="drop"
      class:over={dragOver}
      role="region"
      aria-label="Upload an icon"
      ondragover={(e) => (e.preventDefault(), (dragOver = true))}
      ondragleave={() => (dragOver = false)}
      ondrop={(e) => (e.preventDefault(), (dragOver = false), onFiles(e.dataTransfer?.files))}
    >
      <input bind:this={fileEl} type="file" {accept} class="sr-only" onchange={(e) => onFiles(e.currentTarget.files)} data-testid="ip-file" />
      <button type="button" class="btn primary sm" onclick={() => fileEl?.click()} disabled={uploading} data-testid="ip-upload-btn">
        {#if uploading}<Spinner size={13} />{:else}<Icon icon={Upload} size={14} />{/if} Upload icon
      </button>
      <span class="small muted">or drop a PNG, SVG or WebP here (≤ {bytes(ICON_MAX_BYTES)}). Uploaded icons can be reused everywhere.</span>
    </div>
    {#if upError}<div class="banner error small" role="alert" data-testid="ip-error">{upError}</div>{/if}
    {#if delUsers}
      <div class="banner error small" data-testid="ip-delete-users">
        <div><b>Still used by:</b> {delUsers.users.map((u) => `${u.type} “${u.label}”`).join(', ') || 'other items'}. Pick another icon there first.</div>
      </div>
    {/if}
    {#if upLoading && !uploads}
      <div class="none"><Spinner size={14} /> Loading uploads…</div>
    {:else if !uploads?.length}
      <p class="none">No uploaded icons yet. Upload one — it will be listed here for every form.</p>
    {:else}
      <div class="ugrid">
        {#each uploads as u (u.iconId)}
          {@const v = `upload:${u.iconId}`}
          <div class="up" class:sel={value === v}>
            <button type="button" class="upb" onclick={() => choose(v)} title="{u.name} · {bytes(u.sizeBytes)}" data-testid="ip-upload" data-id={u.iconId} aria-pressed={value === v}>
              <IconView value={v} size={36} />
              <span class="un ellipsis">{u.name}</span>
              <span class="xs faint">{u.usedBy ? `used ${u.usedBy}×` : 'unused'}</span>
            </button>
            {#if !u.usedBy}<button type="button" class="ud" aria-label="Delete {u.name}" title="Delete (unused)" onclick={() => del(u)} data-testid="ip-upload-delete"><Icon icon={Trash2} size={12} /></button>{/if}
          </div>
        {/each}
      </div>
    {/if}
  {/if}
  {#snippet footer()}
    <span class="cur small muted"><IconView value={value} {type} {initials} size={16} round={!!initials} /> Current: {caption}</span>
    <span class="spacer"></span>
    <button type="button" class="btn" onclick={() => (open = false)}><Icon icon={Ban} size={14} />Cancel</button>
  {/snippet}
</Modal>

<style>
  .trig {
    display: flex;
    align-items: center;
    gap: 8px;
    padding: 0 8px 0 4px;
    cursor: pointer;
    text-align: left;
    min-width: 0;
    width: 100%;
    max-width: 320px;
  }
  .trig:disabled {
    cursor: not-allowed;
    background: var(--surface-2);
  }
  .pv {
    width: 28px;
    height: 28px;
    flex: none;
    border-radius: var(--r-sm);
    background: var(--accent-soft);
    color: var(--accent-text);
    display: grid;
    place-items: center;
  }
  .pv.round {
    background: transparent;
    border-radius: 50%;
  }
  .cap {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: var(--fs-md);
  }
  .trig :global(.chev) {
    color: var(--text-3);
  }
  .head {
    display: flex;
    align-items: center;
    gap: 8px;
    flex-wrap: wrap;
    margin-bottom: 10px;
  }
  .btn.sel {
    border-color: var(--accent);
    color: var(--accent-text);
  }
  .sr {
    display: flex;
    align-items: center;
    gap: 8px;
    border: 1px solid var(--border-strong);
    border-radius: var(--r-md);
    padding: 0 10px;
    height: var(--control-h);
    color: var(--text-3);
    margin-bottom: 8px;
  }
  .sr:focus-within {
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .sinput {
    flex: 1;
    border: 0;
    outline: none;
    background: transparent;
    color: var(--text);
    font-size: var(--fs-md);
    min-width: 0;
  }
  .sinput:focus {
    box-shadow: none;
  }
  .grid-wrap {
    max-height: min(52vh, 440px);
    overflow-y: auto;
    padding: 2px;
    outline: none;
  }
  .gh {
    font-size: var(--fs-xs);
    text-transform: uppercase;
    letter-spacing: 0.05em;
    color: var(--text-3);
    margin: 10px 2px 6px;
  }
  .gh:first-child {
    margin-top: 2px;
  }
  .grid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(40px, 1fr));
    gap: 4px;
  }
  .ic {
    height: 40px;
    border: 1px solid transparent;
    border-radius: var(--r-md);
    background: transparent;
    color: var(--text);
    display: grid;
    place-items: center;
    cursor: pointer;
    padding: 0;
  }
  .ic:hover {
    background: var(--surface-hover);
  }
  .ic:focus-visible {
    outline: none;
    border-color: var(--accent);
    box-shadow: var(--focus-ring);
  }
  .ic.sel {
    background: var(--accent-soft);
    color: var(--accent-text);
    border-color: var(--accent);
  }
  .ic :global(svg) {
    display: block;
  }
  .none {
    color: var(--text-3);
    font-size: var(--fs-sm);
    padding: 16px 4px;
    display: flex;
    gap: 8px;
    align-items: center;
  }
  .drop {
    display: flex;
    align-items: center;
    gap: 12px;
    flex-wrap: wrap;
    border: 1.5px dashed var(--border-strong);
    border-radius: var(--r-lg);
    padding: 14px;
    margin-bottom: 10px;
    background: var(--surface-2);
  }
  .drop.over {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .banner {
    margin-bottom: 10px;
  }
  .ugrid {
    display: grid;
    grid-template-columns: repeat(auto-fill, minmax(104px, 1fr));
    gap: 8px;
    max-height: min(44vh, 380px);
    overflow-y: auto;
    padding: 2px;
  }
  .up {
    position: relative;
  }
  .upb {
    width: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    gap: 6px;
    padding: 12px 6px 8px;
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    background: var(--surface);
    cursor: pointer;
    color: var(--text);
    min-width: 0;
  }
  .upb:hover {
    background: var(--surface-hover);
  }
  .up.sel .upb {
    border-color: var(--accent);
    background: var(--accent-soft);
  }
  .un {
    font-size: var(--fs-sm);
    max-width: 100%;
  }
  .ud {
    position: absolute;
    top: 4px;
    right: 4px;
    border: 0;
    border-radius: var(--r-sm);
    background: transparent;
    color: var(--text-3);
    width: 22px;
    height: 22px;
    display: grid;
    place-items: center;
    cursor: pointer;
  }
  .ud:hover {
    background: var(--red-soft);
    color: var(--red-text);
  }
  .cur {
    display: inline-flex;
    align-items: center;
    gap: 6px;
    min-width: 0;
  }
</style>
