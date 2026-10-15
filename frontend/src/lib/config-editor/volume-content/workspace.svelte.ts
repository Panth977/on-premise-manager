/**
 * §21 #101 the state of one volume content workspace (starter content of a template / draft, or a cloud version):
 * the edited tree, edited texts (path → text), bytes picked from the computer, the open file and the folder expansion.
 * Every tree mutation goes through the shared pure ops; an edit error (TreeEditError: exists, bad name, into itself …)
 * comes back as a message.
 *
 * Two modes:
 *  - LIVE (§21 #107, starter content: `new ContentWorkspace(true, sink)`): every edit IS a pending change of the scope's
 *    browser change set — tree ops call `sink.apply(tree)` at once, a text edit is hashed and applied ~300 ms after the
 *    last keystroke (or at `flush()`: blur / leaving), bytes go to `sink.keep` (the scope's PendingBlobs: memory +
 *    IndexedDB). There is no Apply: nothing is uploaded until the scope's Commit. `follow(base, tree)` adopts external
 *    changes of the seed (Discard, restore, rebase, commit); `base` = the head's tree (dirty markers = not committed).
 *  - VERSION (Cloud copy: `new ContentWorkspace(false)`): edits stay here until "Save as new version": `prepare()` hashes
 *    the edited texts and returns the tree + the bytes made in this browser; the caller stages them and saves.
 */
import type { FileRender, VolumeTree, VolumeTreeEntry } from '@opm/shared';
import {
  DEFAULT_FILE_MODE,
  RE_FILE_MODE,
  TreeEditError,
  VOLUME_CONTENT_LIMITS,
  ancestorDirs,
  baseName,
  contentPathError,
  duplicateName,
  joinPath,
  looksLikeText,
  normalizeVolumeTree,
  parentDir,
  treeDelete,
  treeDuplicate,
  treeHasDir,
  treeHasFile,
  treeMkdir,
  treeMove,
  treePutFile,
  volumeTreeIssues,
  type TreeIssue,
} from '@opm/shared';
import { sha256OfBlob } from '../files';
import { blobBytes, decodeText, rememberBytes } from './content';

/** where a LIVE workspace puts its edits (the scope's change set) */
export interface ContentSink {
  /** keep bytes as a pending change (sha256 already computed) */
  keep(sha256: string, bytes: Uint8Array): void | Promise<void>;
  /** pending bytes of a blob (undefined = in the store) */
  get(sha256: string): Uint8Array | undefined;
  /** the edited tree (canonical) → write it into the working config */
  apply(tree: VolumeTree): void;
}
export type LoadedFile = { kind: 'text'; text: string } | { kind: 'binary' } | { kind: 'large' } | { kind: 'error'; message: string } | { kind: 'loading' };
const same = (a: VolumeTree, b: VolumeTree) => JSON.stringify(normalizeVolumeTree(a)) === JSON.stringify(normalizeVolumeTree(b));
const under = (p: string, dir: string) => p === dir || p.startsWith(dir + '/');
const enc = new TextEncoder();

export class ContentWorkspace {
  /** starter content (limits seedMax*, render allowed) or a persisted version (version limits, render none) */
  readonly forSeed: boolean;
  base: VolumeTree = $state({ entries: [], dirs: [] });
  tree: VolumeTree = $state({ entries: [], dirs: [] });
  /** path → text being edited (only paths whose text differs from their blob end up changed on save) */
  texts: Record<string, string> = $state({});
  /** sha256 → loaded state of a blob (text / binary / large / error) */
  loaded: Record<string, LoadedFile> = $state({});
  /** the file in the editor */
  open: string | null = $state(null);
  /** the focused tree node (file or folder; '' = the root) */
  focus = $state('');
  expanded: Record<string, boolean> = $state({});
  /** bytes not uploaded yet (picked files, new empty files, edited texts after commit hashing) */
  #local = new Map<string, Uint8Array>();
  #localShas = $state<string[]>([]);
  readonly sink: ContentSink | null;
  #timer: ReturnType<typeof setTimeout> | null = null;
  #flushing: Promise<void> | null = null;
  /** an edit error of the last text flush (too large …) */
  flushError: string | null = $state(null);

  constructor(forSeed: boolean, sink: ContentSink | null = null) {
    this.forSeed = forSeed;
    this.sink = sink;
  }
  /** LIVE: adopt the seed of the working config when it changed outside this editor (Discard, restore, rebase, commit) */
  follow(base: VolumeTree, tree: VolumeTree) {
    const b = normalizeVolumeTree(base);
    if (!same(this.base, b)) this.base = b;
    if (Object.keys(this.texts).length || this.#flushing) return; // own text edits in flight: they win
    const t = normalizeVolumeTree(tree);
    if (same(this.tree, t)) return;
    this.tree = structuredClone(t);
    if (this.open && !treeHasFile(t, this.open)) this.open = null;
  }
  /** Discard of the change set: drop debounced / in-flight text edits so they are not applied afterwards */
  #gen = 0;
  abandon() {
    this.#gen++;
    if (this.#timer) {
      clearTimeout(this.#timer);
      this.#timer = null;
    }
    this.texts = {};
    this.flushError = null;
  }
  /** LIVE: the tree changed → into the change set */
  #emit() {
    if (this.sink) this.sink.apply(normalizeVolumeTree($state.snapshot(this.tree) as VolumeTree));
  }
  /** LIVE: write every edited text now (blur, leaving, before Commit). Resolves when applied. */
  async flush(): Promise<void> {
    if (!this.sink) return;
    if (this.#timer) {
      clearTimeout(this.#timer);
      this.#timer = null;
    }
    while (this.#flushing) await this.#flushing;
    if (!Object.keys(this.texts).length) return;
    this.#flushing = this.#flushTexts();
    try {
      await this.#flushing;
    } finally {
      this.#flushing = null;
    }
  }
  async #flushTexts() {
    const gen = this.#gen;
    let changed = false;
    for (const [path, text] of Object.entries(this.texts)) {
      const e = this.entry(path);
      if (!e) {
        delete this.texts[path];
        continue;
      }
      if (text === this.savedText(path)) {
        delete this.texts[path];
        continue;
      }
      const bytes = enc.encode(text);
      if (bytes.length > VOLUME_CONTENT_LIMITS.fileMaxBytes) {
        this.flushError = `${path}: larger than ${VOLUME_CONTENT_LIMITS.fileMaxBytes / 1048576} MiB — not applied`;
        continue;
      }
      const sha = await sha256OfBlob(new Blob([bytes as BlobPart]));
      await this.#keep(sha, bytes);
      rememberBytes(sha, bytes);
      this.loaded[sha] = { kind: 'text', text };
      if (gen !== this.#gen) return; // discarded meanwhile
      const cur = this.entry(path);
      if (!cur) continue;
      const err = this.#applyRaw((t) => treePutFile(t, { ...cur, sha256: sha, size: bytes.length }, { overwrite: true }));
      if (err) {
        this.flushError = err;
        continue;
      }
      // typed on meanwhile → keep the newer text pending (the next flush takes it)
      if (this.texts[path] === text) delete this.texts[path];
      changed = true;
      this.flushError = null;
    }
    if (changed && gen === this.#gen) this.#emit();
  }
  reset(tree: VolumeTree) {
    const t = normalizeVolumeTree(tree);
    this.base = t;
    this.tree = structuredClone(t);
    this.texts = {};
    if (this.open && !treeHasFile(t, this.open)) this.open = null;
  }
  get dirty(): boolean {
    return !same(this.base, this.tree) || Object.keys(this.texts).some((p) => this.textChanged(p));
  }
  entry(path: string): VolumeTreeEntry | null {
    return this.tree.entries.find((e) => e.path === path) ?? null;
  }
  /** the saved text of a file (its blob), undefined while not loaded / not text */
  savedText(path: string): string | undefined {
    const e = this.entry(path);
    const l = e ? this.loaded[e.sha256] : undefined;
    return l?.kind === 'text' ? l.text : undefined;
  }
  textChanged(path: string): boolean {
    const t = this.texts[path];
    return t !== undefined && t !== this.savedText(path);
  }
  /** files that differ from the base (new / changed content / mode / render) — dirty markers in the tree */
  changedPaths(): Set<string> {
    const b = new Map(this.base.entries.map((e) => [e.path, e] as const));
    const out = new Set<string>();
    for (const e of this.tree.entries) {
      const o = b.get(e.path);
      if (!o || o.sha256 !== e.sha256 || o.mode !== e.mode || o.render !== e.render || this.textChanged(e.path)) out.add(e.path);
    }
    for (const p of out) for (const d of ancestorDirs(p)) out.add(d);
    for (const d of this.tree.dirs) if (!this.base.dirs.includes(d) && !treeHasDir(this.base, d)) out.add(d);
    return out;
  }
  get issues(): TreeIssue[] {
    return volumeTreeIssues(this.tree, this.forSeed);
  }
  get totalBytes(): number {
    return this.tree.entries.reduce((n, e) => n + (e.size || 0), 0);
  }

  // ───── mutations (all through the shared ops)
  #applyRaw(fn: (t: VolumeTree) => VolumeTree): string | null {
    try {
      this.tree = fn($state.snapshot(this.tree) as VolumeTree);
      return null;
    } catch (e) {
      if (e instanceof TreeEditError) return e.message;
      throw e;
    }
  }
  /** a tree op; LIVE → applied into the change set at once */
  #apply(fn: (t: VolumeTree) => VolumeTree): string | null {
    const err = this.#applyRaw(fn);
    if (!err) this.#emit();
    return err;
  }
  #moveKeys(from: string, to: string, copy = false) {
    const re = (p: string) => to + p.slice(from.length);
    const next: Record<string, string> = {};
    for (const [p, t] of Object.entries(this.texts)) {
      if (under(p, from)) {
        next[re(p)] = t;
        if (copy) next[p] = t;
      } else next[p] = t;
    }
    this.texts = next;
    const ex: Record<string, boolean> = {};
    for (const [p, v] of Object.entries(this.expanded)) {
      if (under(p, from)) {
        ex[re(p)] = v;
        if (copy) ex[p] = v;
      } else ex[p] = v;
    }
    this.expanded = ex;
  }
  #reveal(path: string) {
    for (const d of ancestorDirs(path)) this.expanded[d] = true;
  }
  /** a new empty file in `dir` (opened in the editor) */
  async newFile(dir: string, name: string): Promise<string | null> {
    const path = joinPath(dir, name);
    const pe = contentPathError(path);
    if (pe) return pe;
    const empty = new Uint8Array();
    const sha = await sha256OfBlob(new Blob([empty]));
    await this.#keep(sha, empty);
    this.loaded[sha] = { kind: 'text', text: '' };
    const err = this.#apply((t) => treePutFile(t, { path, sha256: sha, size: 0, mode: DEFAULT_FILE_MODE, render: 'none' }));
    if (err) return err;
    this.#reveal(path);
    this.open = path;
    this.focus = path;
    return null;
  }
  newFolder(dir: string, name: string): string | null {
    const path = joinPath(dir, name);
    const err = this.#apply((t) => treeMkdir(t, path));
    if (err) return err;
    this.#reveal(path);
    this.expanded[path] = true;
    this.focus = path;
    return null;
  }
  /** move / rename a file or folder to the full path `to` */
  move(from: string, to: string): string | null {
    if (from === to) return null;
    const err = this.#apply((t) => treeMove(t, from, to));
    if (err) return err;
    this.#moveKeys(from, to);
    if (this.open && under(this.open, from)) this.open = to + this.open.slice(from.length);
    if (under(this.focus, from)) this.focus = to + this.focus.slice(from.length);
    this.#reveal(to);
    return null;
  }
  rename(path: string, name: string): string | null {
    return this.move(path, joinPath(parentDir(path), name));
  }
  /** move into a folder ('' = the root), keeping the name */
  moveInto(path: string, dir: string): string | null {
    if (dir === path || dir.startsWith(path + '/')) return 'a folder cannot be moved into itself';
    return this.move(path, joinPath(dir, baseName(path)));
  }
  duplicate(path: string): string | null {
    const to = duplicateName(this.tree, path);
    const err = this.#apply((t) => treeDuplicate(t, path, to));
    if (err) return err;
    this.#moveKeys(path, to, true);
    this.focus = to;
    return null;
  }
  remove(path: string): string | null {
    const err = this.#apply((t) => treeDelete(t, path));
    if (err) return err;
    for (const p of Object.keys(this.texts)) if (under(p, path)) delete this.texts[p];
    if (this.open && under(this.open, path)) this.open = null;
    if (under(this.focus, path)) this.focus = parentDir(path);
    return null;
  }
  setMode(path: string, mode: string): string | null {
    if (!RE_FILE_MODE.test(mode)) return 'mode must look like 0644';
    const e = this.entry(path);
    if (!e) return `${path} does not exist`;
    return this.#apply((t) => treePutFile(t, { ...e, mode }, { overwrite: true }));
  }
  setRender(path: string, render: FileRender): string | null {
    const e = this.entry(path);
    if (!e) return `${path} does not exist`;
    return this.#apply((t) => treePutFile(t, { ...e, render }, { overwrite: true }));
  }
  setText(path: string, text: string) {
    this.texts[path] = text;
    if (!this.sink) return;
    if (this.#timer) clearTimeout(this.#timer);
    this.#timer = setTimeout(() => {
      this.#timer = null;
      void this.flush();
    }, 300);
  }
  /** add picked bytes at `path` (overwrite replaces the entry; mode / render of the replaced file are kept) */
  async putBytes(path: string, bytes: Uint8Array, overwrite: boolean): Promise<string | null> {
    if (bytes.length > VOLUME_CONTENT_LIMITS.fileMaxBytes) return `${path}: larger than ${VOLUME_CONTENT_LIMITS.fileMaxBytes / 1048576} MiB (the limit of one file)`;
    const sha = await sha256OfBlob(new Blob([bytes as BlobPart]));
    await this.#keep(sha, bytes);
    rememberBytes(sha, bytes);
    const old = this.entry(path);
    const err = this.#apply((t) => treePutFile(t, { path, sha256: sha, size: bytes.length, mode: old?.mode ?? DEFAULT_FILE_MODE, render: old?.render ?? 'none' }, { overwrite }));
    if (err) return err;
    delete this.texts[path];
    this.#reveal(path);
    return null;
  }
  async #keep(sha: string, bytes: Uint8Array) {
    if (this.sink) return void (await this.sink.keep(sha, bytes));
    this.#local.set(sha, bytes);
    if (!this.#localShas.includes(sha)) this.#localShas = [...this.#localShas, sha];
  }
  #bytes(sha: string): Uint8Array | undefined {
    return this.#local.get(sha) ?? this.sink?.get(sha);
  }
  /** bytes of a file: picked locally, else the blob from the cloud (cached) */
  async bytesOf(path: string): Promise<Uint8Array> {
    const e = this.entry(path);
    if (!e) throw new Error(`${path} does not exist`);
    const t = this.texts[path];
    if (t !== undefined) return enc.encode(t);
    return this.#bytes(e.sha256) ?? (await blobBytes(e.sha256));
  }
  /** load a file for the editor: text ≤ 2 MiB opens; bigger / binary → size + download */
  async load(path: string) {
    const e = this.entry(path);
    if (!e) return;
    const sha = e.sha256;
    const cur = this.loaded[sha];
    if (cur && cur.kind !== 'error') return;
    if (e.size > VOLUME_CONTENT_LIMITS.editorTextMaxBytes) {
      this.loaded[sha] = { kind: 'large' };
      return;
    }
    this.loaded[sha] = { kind: 'loading' };
    try {
      const b = this.#bytes(sha) ?? (await blobBytes(sha));
      this.loaded[sha] = looksLikeText(b) ? { kind: 'text', text: decodeText(b) } : { kind: 'binary' };
    } catch (err) {
      this.loaded[sha] = { kind: 'error', message: err instanceof Error ? err.message : String(err) };
    }
  }
  openFile(path: string) {
    this.open = path;
    this.focus = path;
    void this.load(path);
  }

  /**
   * VERSION mode "Save as new version" step 1: hash every edited text into a new entry (mode / render kept) and return
   * the canonical tree + the bytes made in this browser that the tree uses (the caller stages them — §21 #107 — and
   * saves with the stageId). Nothing is uploaded here. `saved()` forgets the local bytes after a successful save.
   */
  async prepare(): Promise<{ tree: VolumeTree; local: Map<string, Uint8Array> }> {
    for (const [path, text] of Object.entries(this.texts)) {
      if (!this.textChanged(path)) continue;
      const e = this.entry(path);
      if (!e) continue;
      const bytes = enc.encode(text);
      if (bytes.length > VOLUME_CONTENT_LIMITS.fileMaxBytes) throw new Error(`${path}: larger than ${VOLUME_CONTENT_LIMITS.fileMaxBytes / 1048576} MiB`);
      const sha = await sha256OfBlob(new Blob([bytes as BlobPart]));
      await this.#keep(sha, bytes);
      rememberBytes(sha, bytes);
      this.loaded[sha] = { kind: 'text', text };
      const err = this.#applyRaw((t) => treePutFile(t, { ...e, sha256: sha, size: bytes.length }, { overwrite: true }));
      if (err) throw new Error(err);
    }
    this.texts = {};
    const used = new Set(this.tree.entries.map((e) => e.sha256));
    const local = new Map<string, Uint8Array>();
    for (const sha of this.#localShas) if (used.has(sha)) local.set(sha, this.#local.get(sha)!);
    return { tree: normalizeVolumeTree($state.snapshot(this.tree) as VolumeTree), local };
  }
  saved() {
    this.#local.clear();
    this.#localShas = [];
  }
}
