/**
 * migrate-l — §21 #117 persist-to-cloud volume content is committed. Talks to a RUNNING cloud over its API (the
 * Firebase and the VM cloud alike) with an account that may publish the deployments; idempotent.
 *
 * Before #117 the content of a persist volume lived in its cloud copy (versions, Send to site). Now the volume's seed
 * tree IS its committed content. For every deployment (or --deployment <id>) and every persist volume of every site
 * that has a cloud copy, the seed becomes the copy's LATEST version (the content the cloud last held, including an edit
 * that was never sent), render none. All volumes of a deployment go into ONE commit (message MIGRATE_L_MESSAGE) on the
 * current head; secrets keep their values (no plainValue). Volumes whose seed already equals that tree, or whose
 * tree breaks the new rules (excluded paths, over the size cap), are reported and left alone. Pending pushes / snapshot
 * requests are left as they are: agents ≥ 0.19 ignore them.
 *
 * Usage (from backend/):
 *   npx tsx scripts/migrate-l.ts --url https://opm.example.com --token-file ~/.opm-token [--deployment <id>] [--apply]
 * The token is an ID token of the console (POST /auth/login → idToken). Default is the dry run; only --apply commits.
 */
import { readFileSync } from 'node:fs';
import {
  compileExcludes,
  isExcluded,
  normalizeVolumeTree,
  volumeTreeHashInput,
  type Config,
  type ConfigView,
  type DeploymentView,
  type VolumeContentState,
  type VolumeContentVersionResponse,
  type VolumeTree,
} from '@opm/shared';

export const MIGRATE_L_MESSAGE = 'Persist-to-cloud volumes: the content is committed (§21 #117)';

const argv = process.argv.slice(2);
const arg = (n: string) => {
  const i = argv.indexOf(`--${n}`);
  return i >= 0 ? argv[i + 1] : undefined;
};
const apply = argv.includes('--apply');
const base = (arg('url') ?? '').replace(/\/+$/, '');
const tokenFile = arg('token-file');
if (!base || !tokenFile) {
  console.error('usage: migrate-l.ts --url <cloud> --token-file <file> [--deployment <id>] [--apply]');
  process.exit(2);
}
const token = readFileSync(tokenFile.replace(/^~/, process.env.HOME ?? '~'), 'utf8').trim();

async function api<T>(method: string, path: string, body?: unknown): Promise<T> {
  const r = await fetch(base + path, { method, headers: { authorization: `Bearer ${token}`, ...(body ? { 'content-type': 'application/json' } : {}) }, body: body ? JSON.stringify(body) : undefined });
  const text = await r.text();
  if (!r.ok) throw new Error(`${method} ${path} → ${r.status} ${text.slice(0, 400)}`);
  return (text ? JSON.parse(text) : null) as T;
}
const sameTree = (a: VolumeTree, b: VolumeTree) => volumeTreeHashInput(a) === volumeTreeHashInput(b);

/** the head copy as ConfigInput: secrets keep the head's values (no plainValue) */
function toInput(v: ConfigView): Config {
  const c = structuredClone(v) as unknown as Config & { secrets: Record<string, unknown>[] };
  c.secrets = c.secrets.map((s) => {
    const { value: _v, rotatedAt: _r, ...rest } = s;
    void _v, _r;
    return rest;
  }) as never;
  return c;
}

async function deploymentIds(): Promise<string[]> {
  const one = arg('deployment');
  if (one) return [one];
  const out: string[] = [];
  let cursor: string | null = null;
  do {
    const page: { items: { deploymentId: string }[]; cursor?: string | null } = await api('GET', `/v1/deployments?limit=100${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}`);
    out.push(...page.items.map((d) => d.deploymentId));
    cursor = page.cursor ?? null;
  } while (cursor);
  return out;
}

let commits = 0;
for (const id of await deploymentIds()) {
  const dep = await api<DeploymentView>('GET', `/v1/deployments/${id}`);
  const states = (await api<{ items: VolumeContentState[] }>('GET', `/v1/deployments/${id}/volume-content`)).items;
  const cfg = toInput(dep.draft);
  const changed: string[] = [];
  for (const st of states) {
    const site = cfg.sites.find((s) => s.siteSlug === st.siteSlug);
    const vol = site?.config.volumes?.find((v) => v.volumeSlug === st.volumeSlug);
    const where = `${dep.label} · ${st.siteSlug} · ${st.volumeSlug}`;
    if (!vol?.persist) continue;
    if (!st.latest) {
      console.log(`  ${where}: no cloud copy — content stays ${vol.seed ? vol.seed.kind : 'empty'}`);
      continue;
    }
    const v = await api<VolumeContentVersionResponse>('GET', `/v1/deployments/${id}/volume-content/${st.siteSlug}/${st.volumeSlug}/versions/${st.latest.versionId}`);
    const tree = normalizeVolumeTree({ entries: v.tree.entries.map((e) => ({ ...e, render: 'none' as const })), dirs: v.tree.dirs });
    const ex = compileExcludes(vol.persist.exclude);
    const bad = [...tree.entries.filter((e) => isExcluded(e.path, false, ex)).map((e) => e.path), ...tree.dirs.filter((d) => isExcluded(d, true, ex))];
    const total = tree.entries.reduce((n, e) => n + e.size, 0);
    if (bad.length || total > vol.persist.maxBytes) {
      console.log(`  ${where}: SKIPPED — version ${st.latest.seq} ${bad.length ? `holds excluded paths (${bad.slice(0, 5).join(', ')})` : `is ${total} bytes > the size cap ${vol.persist.maxBytes}`}`);
      continue;
    }
    const cur = vol.seed?.kind === 'tree' ? normalizeVolumeTree(vol.seed) : null;
    if (cur && sameTree(cur, tree)) {
      console.log(`  ${where}: already committed (version ${st.latest.seq})`);
      continue;
    }
    const snap = st.lastSnapshot ? ` (last snapshot from the site: version ${st.lastSnapshot.seq}${st.lastSnapshot.versionId === st.latest.versionId ? ', the same' : ''})` : '';
    console.log(`  ${where}: content ← version ${st.latest.seq} (${st.latest.origin}, ${tree.entries.length} files)${snap}`);
    vol.seed = { kind: 'tree', ...tree };
    changed.push(`${st.siteSlug}/${st.volumeSlug}`);
  }
  if (!changed.length) {
    console.log(`${dep.label} (${id}): nothing to commit`);
    continue;
  }
  if (!apply) {
    console.log(`${dep.label} (${id}): would commit ${changed.join(', ')}`);
    continue;
  }
  const c = await api<{ seq: number }>('POST', `/v1/deployments/${id}/commits`, { message: `${MIGRATE_L_MESSAGE}\n\n${changed.join('\n')}`, baseCommitId: dep.branch?.head?.commitId ?? null, config: cfg });
  commits++;
  console.log(`${dep.label} (${id}): committed seq ${c.seq} — ${changed.join(', ')}`);
}
console.log(apply ? `done: ${commits} commit(s)` : 'dry run: nothing written (pass --apply)');
