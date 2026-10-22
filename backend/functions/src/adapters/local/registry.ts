/**
 * Index registry of the local DocStore: which fields of which table get an `ix_<field>` column (string / number as is,
 * boolean 0|1, Timestamp epoch ms, else NULL) and which SQLite indexes exist. Derived from firestore.indexes.json and
 * every where / orderBy in functions/src, scripts and test (incl. the dynamic filters of listActivities, listAuditLog
 * and the collection-group sweeps of jobs/daily.ts). A query on a field missing here falls back to json_extract —
 * and throws in strict mode (OPM_LOCAL_STRICT=1), so tests keep this list complete.
 *
 * Table name = the collection path without document ids, joined by "__" (global file) or below deployments/{id}
 * (deployment file; the deployment document itself is the table "deployment").
 */
export interface TableIndexSpec {
  /** fields filtered (==, in, range) or ordered by → ix columns + (parent, ix) indexes */
  scalar?: string[];
  /** fields queried with array-contains (json_each over the stored JSON, no column) */
  array?: string[];
  /** composite indexes (parent, ix_a, ix_b, …) for the multi-field query shapes */
  composite?: string[][];
  /** fields queried through collectionGroup (no parent equality) → an extra single-column index */
  group?: string[];
}
export type IndexRegistry = Record<string, TableIndexSpec>;

const ACT_TAIL = ['_tsMs', 'seq'];
export const INDEX_REGISTRY: IndexRegistry = {
  // ─── global file
  templates: { scalar: ['slug'] },
  services__images: { scalar: ['registryId', 'createdAt'], group: ['registryId'] },
  templates__commits: { scalar: ['seq'] },
  auditLog: {
    scalar: ['ts', 'target', 'actor', 'actorEmail', 'targetType', 'outcome', 'action', 'requestId', 'op'],
    array: ['_actionKeys'],
    composite: [
      ['target', 'ts'],
      ['actor', 'ts'],
      ['actorEmail', 'ts'],
      ['targetType', 'ts'],
      ['outcome', 'ts'],
    ],
  },
  icons: { scalar: ['sha256'] },
  uploads: { scalar: ['expiresAt'] },
  stages: { scalar: ['expiresAt'] },
  agentNonces: { scalar: ['expiresAt'] },
  rateLimits: { scalar: ['expireAt', 'uid'] },
  githubMirror__queue: { scalar: ['gitBranch', 'state', 'createdAt', 'op', 'reason'], composite: [['gitBranch', 'state']] },
  authRefresh: { scalar: ['uid', 'family', 'expiresAt'] },
  authReset: { scalar: ['uid', 'expiresAt'] },
  authOidcState: { scalar: ['expiresAt'] },
  authOidcCode: { scalar: ['expiresAt'] },
  // ─── deployment files
  deployment: {
    scalar: ['deleted', 'updatedAt', 'slug', 'agentUpgradeRun.status'],
    array: ['tagSlugs', 'managerAccountUids'],
    composite: [['deleted', 'updatedAt']],
  },
  commits: { scalar: ['seq'] },
  agentBranches__commits: { scalar: ['seq'] },
  revisions: { scalar: ['seq'] },
  proposals: { scalar: ['status', 'revId'], group: ['status'] },
  agents: { scalar: ['upgradeRequest.expiresAt'], group: ['upgradeRequest.expiresAt'] },
  installTokens: { scalar: ['tokenHash', 'expiresAt', 'siteSlug'], group: ['tokenHash', 'expiresAt'] },
  activities: {
    scalar: ['_tsMs', 'seq', 'siteSlug', 'actor', 'origin', 'uploadedVia', 'type'],
    array: ['_typeKeys'],
    composite: [ACT_TAIL, ['siteSlug', ...ACT_TAIL], ['actor', ...ACT_TAIL], ['origin', ...ACT_TAIL], ['uploadedVia', ...ACT_TAIL]],
  },
  transfers: {
    scalar: ['siteSlug', 'direction', 'status', 'createdAt', 'expiresAt'],
    composite: [
      ['siteSlug', 'direction', 'createdAt'],
      ['direction', 'status'],
    ],
    group: ['expiresAt'],
  },
  // §21 #123 shared files: the cloud view (state in …, unpublishedAt ≥ tombstone cutoff)
  files: { scalar: ['state', 'unpublishedAt'] },
  uptime: { scalar: ['date'], group: ['date'] },
  volumeContent: { scalar: ['_open', 'siteSlug', 'versionCount'], composite: [['siteSlug', '_open']], group: ['versionCount'] },
  volumeContent__versions: { scalar: ['origin', 'seq', 'agentSnapshotId'] },
};

export const ixColumn = (field: string) => `ix_${field.replace(/\./g, '_')}`;

/** registry + extra entries (tests / future collections) merged per table */
export function mergeRegistry(base: IndexRegistry, extra?: IndexRegistry): IndexRegistry {
  if (!extra) return base;
  const out: IndexRegistry = { ...base };
  for (const [t, s] of Object.entries(extra)) {
    const b = out[t] ?? {};
    out[t] = {
      scalar: [...new Set([...(b.scalar ?? []), ...(s.scalar ?? [])])],
      array: [...new Set([...(b.array ?? []), ...(s.array ?? [])])],
      composite: [...(b.composite ?? []), ...(s.composite ?? [])],
      group: [...new Set([...(b.group ?? []), ...(s.group ?? [])])],
    };
  }
  return out;
}
