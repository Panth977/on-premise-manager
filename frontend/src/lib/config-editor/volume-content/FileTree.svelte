<script lang="ts" module>
  export type TreeAction = 'new-file' | 'new-folder' | 'rename' | 'move' | 'duplicate' | 'delete' | 'upload-files' | 'upload-folder' | 'download';
</script>

<script lang="ts">
  /**
   * §21 #101 file tree of a volume's content: folders first, expand / collapse, roving keyboard focus (↑ ↓ → ← Home End
   * Enter, F2 rename, Delete, Shift+F10 / ContextMenu key = menu), right-click menu, drag-and-drop moves inside the tree
   * and file / folder drops from the computer onto a folder (or the empty area = the root). Dirty markers per file.
   */
  import { tick } from 'svelte';
  import { formatContentBytes, parentDir, treeNodes, type TreeNode } from '@opm/shared';
  import Icon from '../../ui/Icon.svelte';
  import { portal } from '../../ui/portal';
  import { ChevronRight, ChevronDown, Folder, FolderOpen, File, FileCode, FilePlus, FolderPlus, Pencil, FolderInput, Copy, Trash2, Upload, FolderUp, Download } from '../../ui/icons';
  import type { IconNode } from '../../ui/icons';
  import { toast } from '../../toast.svelte';
  import type { ContentWorkspace } from './workspace.svelte';
  import { languageForPath } from '@opm/shared';
  import { pickedFromDrop, type PickedFile } from './content';

  type Props = {
    ws: ContentWorkspace;
    readOnly?: boolean;
    /** paths excluded by the persist excludes (marked) */
    excluded?: Set<string>;
    label?: string;
    onaction: (a: TreeAction, path: string) => void;
    ondropfiles: (dir: string, files: PickedFile[]) => void;
  };
  let { ws, readOnly = false, excluded = new Set(), label = 'Files', onaction, ondropfiles }: Props = $props();

  type Row = { node: TreeNode; depth: number };
  const rows = $derived.by(() => {
    const out: Row[] = [];
    const walk = (ns: TreeNode[], depth: number) => {
      for (const n of ns) {
        out.push({ node: n, depth });
        if (n.dir && ws.expanded[n.path]) walk(n.children, depth + 1);
      }
    };
    walk(treeNodes(ws.tree), 0);
    return out;
  });
  const changed = $derived(ws.changedPaths());
  let root = $state<HTMLElement | null>(null);

  function focusRow(path: string) {
    ws.focus = path;
    tick().then(() => root?.querySelector<HTMLElement>(`[data-path="${CSS.escape(path)}"]`)?.focus());
  }
  function activate(n: TreeNode) {
    if (n.dir) ws.expanded[n.path] = !ws.expanded[n.path];
    else ws.openFile(n.path);
    ws.focus = n.path;
  }
  function onkey(e: KeyboardEvent, r: Row, i: number) {
    const n = r.node;
    const go = (k: number) => {
      const t = rows[Math.max(0, Math.min(rows.length - 1, k))];
      if (t) focusRow(t.node.path);
    };
    switch (e.key) {
      case 'ArrowDown':
        go(i + 1);
        break;
      case 'ArrowUp':
        go(i - 1);
        break;
      case 'Home':
        go(0);
        break;
      case 'End':
        go(rows.length - 1);
        break;
      case 'ArrowRight':
        if (n.dir && !ws.expanded[n.path]) ws.expanded[n.path] = true;
        else if (n.dir && n.children.length) go(i + 1);
        break;
      case 'ArrowLeft':
        if (n.dir && ws.expanded[n.path]) ws.expanded[n.path] = false;
        else if (parentDir(n.path)) focusRow(parentDir(n.path));
        break;
      case 'Enter':
      case ' ':
        activate(n);
        break;
      case 'F2':
        if (!readOnly) onaction('rename', n.path);
        break;
      case 'Delete':
        if (!readOnly) onaction('delete', n.path);
        break;
      case 'ContextMenu':
        openMenuAt(n.path, (e.currentTarget as HTMLElement).getBoundingClientRect());
        break;
      case 'F10':
        if (e.shiftKey) openMenuAt(n.path, (e.currentTarget as HTMLElement).getBoundingClientRect());
        else return;
        break;
      default:
        return;
    }
    e.preventDefault();
  }

  // ───── context menu
  let menu = $state<{ path: string; dir: boolean; x: number; y: number } | null>(null);
  let menuEl = $state<HTMLElement | null>(null);
  type MI = { label: string; icon: IconNode; act: TreeAction; danger?: boolean; testid: string };
  const menuItems = $derived.by<MI[]>(() => {
    if (!menu) return [];
    const isRoot = menu.path === '';
    const items: MI[] = [];
    if (!readOnly && (menu.dir || isRoot)) {
      items.push({ label: 'New file', icon: FilePlus, act: 'new-file', testid: 'vc-menu-new-file' }, { label: 'New folder', icon: FolderPlus, act: 'new-folder', testid: 'vc-menu-new-folder' }, { label: 'Upload files…', icon: Upload, act: 'upload-files', testid: 'vc-menu-upload-files' }, { label: 'Upload a folder…', icon: FolderUp, act: 'upload-folder', testid: 'vc-menu-upload-folder' });
    }
    if (!isRoot && !menu.dir) items.push({ label: 'Download', icon: Download, act: 'download', testid: 'vc-menu-download' });
    if (!readOnly && !isRoot)
      items.push(
        { label: 'Rename…', icon: Pencil, act: 'rename', testid: 'vc-menu-rename' },
        { label: 'Move to…', icon: FolderInput, act: 'move', testid: 'vc-menu-move' },
        { label: 'Duplicate', icon: Copy, act: 'duplicate', testid: 'vc-menu-duplicate' },
        { label: 'Delete…', icon: Trash2, act: 'delete', danger: true, testid: 'vc-menu-delete' },
      );
    return items;
  });
  async function openMenuAt(path: string, r: { left: number; bottom: number } | null, x?: number, y?: number) {
    const dir = path === '' || !ws.entry(path);
    menu = { path, dir, x: x ?? (r ? r.left + 24 : 0), y: y ?? (r ? r.bottom : 0) };
    await tick();
    if (menuEl) {
      const vw = document.documentElement.clientWidth;
      const vh = window.innerHeight;
      const b = menuEl.getBoundingClientRect();
      menuEl.style.left = `${Math.max(8, Math.min(menu.x, vw - b.width - 8))}px`;
      menuEl.style.top = `${Math.max(8, menu.y + b.height > vh - 8 ? menu.y - b.height : menu.y)}px`;
      menuEl.querySelector<HTMLElement>('[role="menuitem"]')?.focus();
    }
  }
  function oncontext(e: MouseEvent, path: string) {
    e.preventDefault();
    e.stopPropagation();
    ws.focus = path;
    openMenuAt(path, null, e.clientX, e.clientY);
  }
  function closeMenu(refocus = true) {
    const p = menu?.path ?? null;
    menu = null;
    if (refocus && p) focusRow(p);
  }
  function pick(it: MI) {
    const p = menu!.path;
    closeMenu(false);
    onaction(it.act, p);
  }
  function menuKey(e: KeyboardEvent) {
    const els = Array.from(menuEl?.querySelectorAll<HTMLElement>('[role="menuitem"]') ?? []);
    const i = els.indexOf(document.activeElement as HTMLElement);
    if (e.key === 'ArrowDown') els[(i + 1) % els.length]?.focus();
    else if (e.key === 'ArrowUp') els[(i - 1 + els.length) % els.length]?.focus();
    else if (e.key === 'Escape') {
      e.stopPropagation();
      closeMenu();
    } else if (e.key === 'Tab') closeMenu(false);
    else return;
    e.preventDefault();
  }

  // ───── drag and drop
  const MIME = 'application/x-opm-path';
  let dragging = $state<string | null>(null);
  let over = $state<string | null>(null);
  const dropDir = (n: TreeNode | null) => (n ? (n.dir ? n.path : parentDir(n.path)) : '');
  function ondragstart(e: DragEvent, n: TreeNode) {
    if (readOnly) return e.preventDefault();
    dragging = n.path;
    e.dataTransfer?.setData(MIME, n.path);
    e.dataTransfer?.setData('text/plain', n.path);
    if (e.dataTransfer) e.dataTransfer.effectAllowed = 'move';
  }
  function accepts(e: DragEvent) {
    const types = Array.from(e.dataTransfer?.types ?? []);
    return !readOnly && (types.includes(MIME) || types.includes('Files'));
  }
  function ondragover(e: DragEvent, n: TreeNode | null) {
    if (!accepts(e)) return;
    e.preventDefault();
    e.stopPropagation();
    over = dropDir(n);
    if (e.dataTransfer) e.dataTransfer.dropEffect = Array.from(e.dataTransfer.types).includes(MIME) ? 'move' : 'copy';
  }
  async function ondrop(e: DragEvent, n: TreeNode | null) {
    if (!accepts(e)) return;
    e.preventDefault();
    e.stopPropagation();
    const dir = dropDir(n);
    over = null;
    const src = e.dataTransfer?.getData(MIME) || dragging;
    dragging = null;
    if (src) {
      if (parentDir(src) === dir) return;
      const err = ws.moveInto(src, dir);
      if (err) toast.error(err, 'Not moved');
      return;
    }
    if (e.dataTransfer?.files.length) ondropfiles(dir, await pickedFromDrop(e.dataTransfer));
  }
  const fileIcon = (p: string) => (languageForPath(p) ? FileCode : File);
</script>

<svelte:window onclick={() => menu && closeMenu(false)} onblur={() => menu && closeMenu(false)} />

<div
  class="tree"
  class:over-root={over === ''}
  role="tree"
  aria-label={label}
  tabindex={rows.length ? -1 : 0}
  bind:this={root}
  data-testid="vc-tree"
  ondragover={(e) => ondragover(e, null)}
  ondragleave={(e) => {
    if (e.currentTarget === e.target) over = null;
  }}
  ondrop={(e) => ondrop(e, null)}
  oncontextmenu={(e) => oncontext(e, '')}
>
  {#each rows as r, i (r.node.path)}
    {@const n = r.node}
    {@const ex = excluded.has(n.path)}
    <div
      class="row"
      class:sel={ws.open === n.path}
      class:focus={ws.focus === n.path}
      class:over={over === n.path && n.dir}
      class:ex
      class:dragging={dragging === n.path}
      role="treeitem"
      aria-level={r.depth + 1}
      aria-expanded={n.dir ? !!ws.expanded[n.path] : undefined}
      aria-selected={ws.open === n.path}
      tabindex={ws.focus === n.path || (ws.focus === '' && i === 0) ? 0 : -1}
      style="--d:{r.depth}"
      draggable={!readOnly}
      data-path={n.path}
      data-dir={n.dir ? 'true' : 'false'}
      data-testid="vc-node"
      onclick={() => activate(n)}
      onkeydown={(e) => onkey(e, r, i)}
      onfocus={() => (ws.focus = n.path)}
      oncontextmenu={(e) => oncontext(e, n.path)}
      ondragstart={(e) => ondragstart(e, n)}
      ondragend={() => ((dragging = null), (over = null))}
      ondragover={(e) => ondragover(e, n)}
      ondrop={(e) => ondrop(e, n)}
    >
      <span class="tw">{#if n.dir}<Icon icon={ws.expanded[n.path] ? ChevronDown : ChevronRight} size={13} />{/if}</span>
      <Icon icon={n.dir ? (ws.expanded[n.path] ? FolderOpen : Folder) : fileIcon(n.path)} size={14} class={n.dir ? 'fi dir' : 'fi'} />
      <span class="nm mono">{n.name}</span>
      {#if changed.has(n.path)}<span class="dot" title="Changed (not saved)" data-testid="vc-dirty"></span>{/if}
      {#if ex}<span class="badge outline xs exb" title="Matches an exclude pattern of Persist to cloud">excluded</span>{/if}
      {#if n.entry}
        {#if n.entry.render === 'expressions'}<span class="xb" title="Renders {'{{ }}'} expressions once per site">{'{ }'}</span>{/if}
        <span class="sz">{formatContentBytes(n.entry.size)}</span>
      {:else if n.dir && !n.children.length}<span class="sz faint">empty</span>{/if}
    </div>
  {:else}
    <div class="empty small faint" data-testid="vc-tree-empty">No files yet.{#if !readOnly} Create one, or drop files and folders here.{/if}</div>
  {/each}
</div>

{#if menu && menuItems.length}
  <div class="cmenu" role="menu" tabindex="-1" bind:this={menuEl} use:portal onkeydown={menuKey} onclick={(e) => e.stopPropagation()} data-testid="vc-menu" style="left:{menu.x}px;top:{menu.y}px">
    <div class="mh xs faint mono">{menu.path || '/ (root)'}</div>
    {#each menuItems as it (it.act)}
      <button type="button" role="menuitem" class="it" class:danger={it.danger} tabindex="-1" onclick={() => pick(it)} data-testid={it.testid}><Icon icon={it.icon} size={14} />{it.label}</button>
    {/each}
  </div>
{/if}

<style>
  .tree {
    display: flex;
    flex-direction: column;
    padding: 4px;
    min-height: 100%;
    outline: none;
    border-radius: var(--r-md);
  }
  .tree.over-root {
    background: color-mix(in srgb, var(--accent-soft) 60%, transparent);
    box-shadow: inset 0 0 0 1px var(--accent);
  }
  .row {
    display: flex;
    align-items: center;
    gap: 5px;
    padding: 3px 6px 3px calc(6px + var(--d) * 14px);
    border-radius: var(--r-sm);
    cursor: pointer;
    font-size: var(--fs-sm);
    min-height: 26px;
    outline: none;
    user-select: none;
    color: var(--text);
  }
  .row:hover {
    background: var(--surface-hover);
  }
  .row.focus:focus-visible {
    box-shadow: inset 0 0 0 2px var(--accent);
  }
  .row.sel {
    background: var(--accent-soft);
  }
  .row.over {
    background: color-mix(in srgb, var(--accent-soft) 80%, transparent);
    box-shadow: inset 0 0 0 1px var(--accent);
  }
  .row.dragging {
    opacity: 0.5;
  }
  .row.ex .nm {
    color: var(--text-3);
    text-decoration: line-through;
  }
  .tw {
    width: 13px;
    display: inline-flex;
    color: var(--text-3);
    flex: none;
  }
  .row :global(.fi) {
    flex: none;
    color: var(--text-2);
  }
  .row :global(.fi.dir) {
    color: var(--accent);
  }
  .nm {
    flex: 1;
    min-width: 0;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
    font-size: 12.5px;
  }
  .sz {
    flex: none;
    font-size: 11px;
    color: var(--text-3);
    font-variant-numeric: tabular-nums;
  }
  .xb {
    flex: none;
    font-family: var(--font-mono);
    font-size: 10px;
    color: var(--accent-text);
    background: var(--accent-soft);
    border-radius: 3px;
    padding: 0 3px;
  }
  .exb {
    flex: none;
    font-size: 10px;
    padding: 0 5px;
  }
  .dot {
    width: 7px;
    height: 7px;
    border-radius: 50%;
    background: var(--amber);
    flex: none;
  }
  .empty {
    padding: 14px 10px;
  }
  .cmenu {
    position: fixed;
    z-index: var(--z-popover);
    min-width: 200px;
    background: var(--surface);
    border: 1px solid var(--border);
    border-radius: var(--r-md);
    box-shadow: var(--shadow-lg);
    padding: 4px;
  }
  .mh {
    padding: 4px 9px 6px;
    border-bottom: 1px solid var(--border);
    margin-bottom: 4px;
    max-width: 280px;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .it {
    display: flex;
    align-items: center;
    gap: 8px;
    width: 100%;
    padding: 7px 9px;
    border: 0;
    background: transparent;
    border-radius: var(--r-sm);
    color: var(--text);
    font-size: var(--fs-md);
    cursor: pointer;
    text-align: left;
    white-space: nowrap;
  }
  .it:hover,
  .it:focus {
    background: var(--surface-hover);
    outline: none;
  }
  .it.danger {
    color: var(--red-text);
  }
</style>
