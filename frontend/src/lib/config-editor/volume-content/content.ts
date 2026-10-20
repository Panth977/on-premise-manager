/**
 * §21 #101 volume content helpers of the cloud console: language extensions by extension (CodeMirror), blob text cache,
 * folder / file drops (webkitRelativePath kept), exclude previews, the agent-version warning, legacy seed conversion.
 * Tree mutations are the shared pure ops (treePutFile / treeMkdir / treeMove / treeDelete / treeDuplicate).
 */
import type { Extension } from '@codemirror/state';
import type { Config, Site, SiteConfig, VolumeSeed, VolumeTree } from '@opm/shared';
import { VOLUME_CONTENT_COMMIT_MIN_AGENT, VOLUME_CONTENT_MIN_AGENT, compareAgentVersions, compileExcludes, isExcluded, languageForPath, migrateVolumeFileSeeds, normalizeVolumeTree, parentDir, treeNodes, usesVolumeContent, type TreeNode } from '@opm/shared';
import { fetchContent } from '../files';
import { catalog } from '../../stores/catalog.svelte';

export type ContentLang = ReturnType<typeof languageForPath>;

/** CodeMirror language support for a path (lazy chunks; null = plain text) */
export async function languageExtension(path: string): Promise<Extension> {
  const id = languageForPath(path);
  switch (id) {
    case 'json':
      return (await import('@codemirror/lang-json')).json();
    case 'javascript':
      return (await import('@codemirror/lang-javascript')).javascript({ jsx: /\.jsx$/i.test(path) });
    case 'typescript':
      return (await import('@codemirror/lang-javascript')).javascript({ typescript: true, jsx: /\.tsx$/i.test(path) });
    case 'yaml':
      return (await import('@codemirror/lang-yaml')).yaml();
    case 'html':
      return (await import('@codemirror/lang-html')).html();
    case 'css':
      return (await import('@codemirror/lang-css')).css();
    case 'markdown':
      return (await import('@codemirror/lang-markdown')).markdown();
    case 'xml':
      return (await import('@codemirror/lang-xml')).xml();
    case 'python':
      return (await import('@codemirror/lang-python')).python();
    case 'sql':
      return (await import('@codemirror/lang-sql')).sql();
    case 'shell': {
      const [{ StreamLanguage }, { shell }] = await Promise.all([import('@codemirror/language'), import('@codemirror/legacy-modes/mode/shell')]);
      return StreamLanguage.define(shell as unknown as Parameters<typeof StreamLanguage.define>[0]);
    }
    case 'ini': {
      const [{ StreamLanguage }, m] = await Promise.all([import('@codemirror/language'), /\.toml$/i.test(path) ? import('@codemirror/legacy-modes/mode/toml') : import('@codemirror/legacy-modes/mode/properties')]);
      return StreamLanguage.define(('toml' in m ? m.toml : m.properties) as unknown as Parameters<typeof StreamLanguage.define>[0]);
    }
    default:
      return [];
  }
}
export const LANG_LABEL: Record<NonNullable<ContentLang>, string> = { json: 'JSON', javascript: 'JavaScript', typescript: 'TypeScript', yaml: 'YAML', html: 'HTML', css: 'CSS', markdown: 'Markdown', xml: 'XML', python: 'Python', sql: 'SQL', shell: 'Shell', ini: 'INI / properties' };

/** bytes of a blob (cached in memory: content-addressed, immutable) */
const bytesCache = new Map<string, Promise<Uint8Array>>();
export function blobBytes(sha256: string): Promise<Uint8Array> {
  let p = bytesCache.get(sha256);
  if (!p) {
    p = fetchContent(sha256).then(async (b) => new Uint8Array(await b.arrayBuffer()));
    p.catch(() => bytesCache.delete(sha256));
    bytesCache.set(sha256, p);
  }
  return p;
}
export function rememberBytes(sha256: string, bytes: Uint8Array) {
  bytesCache.set(sha256, Promise.resolve(bytes));
}

/** one file picked / dropped: the path relative to the drop target (folder uploads keep webkitRelativePath) */
export interface PickedFile {
  rel: string;
  file: File;
}
/** files of an <input type=file> (multiple or webkitdirectory) */
export function pickedFromInput(list: FileList | null): PickedFile[] {
  return Array.from(list ?? []).map((f) => ({ rel: (f as File & { webkitRelativePath?: string }).webkitRelativePath || f.name, file: f }));
}
/** files of a drop (folders walked recursively; their relative paths kept) */
export async function pickedFromDrop(dt: DataTransfer): Promise<PickedFile[]> {
  const items = Array.from(dt.items ?? []).filter((i) => i.kind === 'file');
  const entries = items.map((i) => (i as DataTransferItem & { webkitGetAsEntry?: () => FileSystemEntry | null }).webkitGetAsEntry?.() ?? null);
  if (!entries.some((e) => e)) return Array.from(dt.files).map((f) => ({ rel: f.name, file: f }));
  const out: PickedFile[] = [];
  const walk = async (e: FileSystemEntry, prefix: string): Promise<void> => {
    if (e.isFile) {
      const f = await new Promise<File>((res, rej) => (e as FileSystemFileEntry).file(res, rej));
      out.push({ rel: prefix + e.name, file: f });
    } else if (e.isDirectory) {
      const reader = (e as FileSystemDirectoryEntry).createReader();
      for (;;) {
        const batch = await new Promise<FileSystemEntry[]>((res, rej) => reader.readEntries(res, rej));
        if (!batch.length) break;
        for (const c of batch) await walk(c, `${prefix}${e.name}/`);
      }
    }
  };
  for (const e of entries) if (e) await walk(e, '');
  return out;
}

/** what exclude patterns hide in a tree — shared with the persist form ($opm, §21 #102) */
export { excludedIn } from '$opm/forms/volumes/PersistForm.svelte';

/** every folder of a tree (implied + empty), sorted */
export function allDirs(tree: VolumeTree): string[] {
  const out = new Set<string>();
  const walk = (ns: TreeNode[]) => {
    for (const n of ns)
      if (n.dir) {
        out.add(n.path);
        walk(n.children);
      }
  };
  walk(treeNodes(tree));
  return [...out].sort();
}

/** the starter content of a seed as a tree (single = one entry at SINGLE_PATH; legacy / archive → null) */
export const SINGLE_PATH = 'content';
export function seedTree(seed: VolumeSeed | null | undefined, single: boolean): VolumeTree | null {
  if (!seed) return { entries: [], dirs: [] };
  if (seed.kind === 'tree') return normalizeVolumeTree(seed);
  if (seed.kind === 'single' && single) return { entries: [{ path: SINGLE_PATH, sha256: seed.sha256, size: seed.size, mode: seed.mode, render: seed.render }], dirs: [] };
  return null;
}

/** agent < 0.12.0 on a site whose config uses volume content / persist → that site refuses the revision (warning only) */
export interface OldContentAgent {
  siteSlug: string;
  agentVersion: string;
}
function agentCore(v: string): string | null {
  return /^v?(\d+\.\d+\.\d+)/.exec(v.trim())?.[1] ?? null;
}
export function agentSupportsVolumeContent(agentVersion: string | null | undefined): boolean {
  if (!agentVersion) return true;
  const c = agentCore(agentVersion);
  return !c || compareAgentVersions(c, VOLUME_CONTENT_MIN_AGENT) >= 0;
}
/** §21 #117 agents that write a persist volume's committed content into the live volume on merge */
export function agentWritesPersistContent(agentVersion: string | null | undefined): boolean {
  if (!agentVersion) return true;
  const c = agentCore(agentVersion);
  return !c || compareAgentVersions(c, VOLUME_CONTENT_COMMIT_MIN_AGENT) >= 0;
}
export function persistAgentWarning(siteSlug: string, agentVersion: string): string {
  return `Site ${siteSlug} runs agent ${agentVersion}: it writes this content only into an empty volume. Agent ${VOLUME_CONTENT_COMMIT_MIN_AGENT} or newer writes it into the volume when the site merges the commit.`;
}
export function oldContentAgentSites(sites: readonly Pick<Site, 'siteSlug' | 'config'>[], versions: readonly { siteSlug: string; agentVersion: string | null }[]): OldContentAgent[] {
  const out: OldContentAgent[] = [];
  for (const s of sites) {
    if (!usesVolumeContent(s.config)) continue;
    const v = versions.find((x) => x.siteSlug === s.siteSlug)?.agentVersion ?? null;
    if (v && !agentSupportsVolumeContent(v)) out.push({ siteSlug: s.siteSlug, agentVersion: v });
  }
  return out;
}
export function contentAgentWarning(o: OldContentAgent): string {
  return `Site ${o.siteSlug} runs agent ${o.agentVersion}; volume content and persist-to-cloud volumes need agent ${VOLUME_CONTENT_MIN_AGENT} or newer — upgrade the agent first, otherwise the site refuses the revision.`;
}

/** shared migrateVolumeFileSeeds on ONE site (images from the catalog); returns the converted site config + what changed */
export function convertSiteSeeds(files: Config['files'], site: Site) {
  // plain copies: reactive proxies cannot be structuredClone'd by the shared migration
  const plain = <T>(v: T): T => JSON.parse(JSON.stringify(v)) as T;
  const r = migrateVolumeFileSeeds({ files: plain(files), sites: [plain(site)] }, (svc, ver) => catalog.image(svc, ver)?.volumes.map((v) => ({ name: v.name, mountPath: v.mountPath })) ?? null);
  return { config: r.config.sites[0]!.config as SiteConfig, changed: r.changed, lines: r.lines, notes: r.notes, moved: r.moved, removedFiles: r.removedFiles };
}

/** text of bytes (UTF-8, replacement chars for invalid sequences) */
export const decodeText = (b: Uint8Array) => new TextDecoder().decode(b);
