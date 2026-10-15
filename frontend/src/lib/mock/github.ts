/**
 * §21 #106 mock of the GitHub mirror (VITE_MOCK=1): settings + token (masked, reveal 60 s), Test connection, the per-branch
 * status table (derived from the mock branch store: templates, deployments, agent branches, uncommitted drift), Sync now /
 * Resync everything (time-based: pushing → synced after ~1.5 s). Test hook: window.__opmMockGithub(state).
 */
import {
  GITHUB_OWNER_RE,
  GITHUB_REPO_RE,
  GITHUB_TOKEN_RE,
  MIRROR_EXPORT_FORMAT,
  REVEAL_SECONDS,
  maskGithubToken,
  mirrorBranchName,
  branchKey,
  type BranchRef,
  type Commit,
  type ErrorCode,
  type ExportNote,
  type GithubMirrorDoc,
  type GithubMirrorView,
  type GithubTestResponse,
  type MirrorBranchKind,
  type MirrorBranchRef,
  type MirrorBranchState,
  type MirrorQueueItem,
  type ValidationIssue,
  type AgentBranchDoc,
} from "@opm/shared";
import { getDb, saveNow, type MockDb } from "./db";

interface Row {
  pushedSeq: number | null;
  pushedId: string | null;
  pushedAt: string | null;
  gitSha: string | null;
  error: { at: string; message: string; status: number | null } | null;
  attempts: number;
  notes: ExportNote[];
  /** pushing until (ms); then synced */
  syncAt?: number;
  /** the next push fails (mock) */
  failNext?: boolean;
}
export interface MockGithub {
  doc: GithubMirrorDoc;
  rows: Record<string, Row>;
  seeded: boolean;
}
type Store = {
  commits: Record<string, Commit[]>;
  agentBranches: Record<string, Record<string, AgentBranchDoc>>;
};
const brStore = (db: MockDb): Store =>
  ((db as MockDb & { br?: Store }).br ?? {
    commits: {},
    agentBranches: {},
  }) as Store;
const gh = (db: MockDb): MockGithub =>
  (db as MockDb & { github: MockGithub }).github;

function hex40(s: string): string {
  let h1 = 0x811c9dc5,
    h2 = 0x01000193,
    h3 = 0x9e3779b9;
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    h1 = Math.imul(h1 ^ c, 16777619) >>> 0;
    h2 = Math.imul(h2 + c, 2246822519) >>> 0;
    h3 = Math.imul(h3 ^ (c + i), 3266489917) >>> 0;
  }
  return (
    h1.toString(16).padStart(8, "0") +
    h2.toString(16).padStart(8, "0") +
    h3.toString(16).padStart(8, "0")
  )
    .repeat(2)
    .slice(0, 40);
}
const iso = (ms: number) => new Date(ms).toISOString();
const isoAgo = (ms: number) => iso(Date.now() - ms);

interface Branch {
  gitBranch: string;
  ref: MirrorBranchRef;
  label: string;
  commits: Commit[];
  /** uncommitted rows: the drift count instead of commits */
  drift?: number;
}
/** every mirrored branch of the mock db (template / deployment / agent / uncommitted) */
function branches(db: MockDb): Branch[] {
  const st = brStore(db);
  const out: Branch[] = [];
  const list = (b: BranchRef) => st.commits[branchKey(b)] ?? [];
  for (const t of Object.values(db.templates).sort((a, b) =>
    a.label.localeCompare(b.label),
  )) {
    if (!t.slug) continue;
    out.push({
      gitBranch: mirrorBranchName("template", t.slug),
      ref: {
        kind: "template",
        templateId: t.templateId,
        deploymentId: null,
        siteSlug: null,
      },
      label: t.label,
      commits: list({ kind: "template", templateId: t.templateId }),
    });
  }
  for (const d of Object.values(db.deployments).sort((a, b) =>
    a.label.localeCompare(b.label),
  )) {
    if (d.deleted || !d.slug) continue;
    out.push({
      gitBranch: mirrorBranchName("deployment", d.slug),
      ref: {
        kind: "deployment",
        templateId: null,
        deploymentId: d.deploymentId,
        siteSlug: null,
      },
      label: d.label,
      commits: list({ kind: "deployment", deploymentId: d.deploymentId }),
    });
    const ab = st.agentBranches[d.deploymentId] ?? {};
    for (const site of Object.keys(ab).sort()) {
      const a = ab[site]!;
      out.push({
        gitBranch: mirrorBranchName("agent", d.slug, site),
        ref: {
          kind: "agent",
          templateId: null,
          deploymentId: d.deploymentId,
          siteSlug: site,
        },
        label: `${d.label} · ${site}`,
        commits: list({
          kind: "agent",
          deploymentId: d.deploymentId,
          siteSlug: site,
        }),
      });
      const drift = (a.uncommitted?.drift ?? 0) + (a.uncommitted?.notes ?? 0);
      if (drift > 0)
        out.push({
          gitBranch: mirrorBranchName("uncommitted", d.slug, site),
          ref: {
            kind: "uncommitted",
            templateId: null,
            deploymentId: d.deploymentId,
            siteSlug: site,
          },
          label: `${d.label} · ${site} (uncommitted)`,
          commits: [],
          drift,
        });
    }
  }
  return out;
}

const SEED_NOTES: ExportNote[] = [
  {
    code: "HEALTH_LOG",
    site: "app",
    message:
      'container worker: the "log" health check has no compose equivalent — dependents wait for "started" only',
  },
  {
    code: "NO_DRIFT",
    site: null,
    message:
      "without the agent nothing detects manual changes, rolls back, snapshots or persists volumes to the cloud",
  },
];

/** seed (once) + rows for branches created later */
export function ensureGithub(db: MockDb) {
  const x = db as MockDb & { github?: MockGithub };
  if (!x.github) {
    const t = isoAgo(6 * 86400_000);
    x.github = {
      doc: {
        enabled: true,
        owner: "acme-ops",
        repo: "opm-mirror",
        token: "github_pat_11ACMEOPS0mockTokenValue_9f8e7d6c5b4a3210zYxW",
        tokenSetAt: t,
        tokenSetBy: "uAdmin00000000000001",
        initializedAt: t,
        lastTest: null,
        pausedUntil: null,
        pauseReason: null,
        createdAt: t,
        createdBy: "uAdmin00000000000001",
        updatedAt: t,
        updatedBy: "uAdmin00000000000001",
      },
      rows: {},
      seeded: false,
    };
  }
  const g = x.github;
  const all = branches(db);
  for (const [i, b] of all.entries()) {
    if (g.rows[b.gitBranch]) continue;
    const head = b.commits.at(-1) ?? null;
    if (!g.seeded) {
      // seed scenario: everything synced, one agent branch failing, one template never pushed (queued), notes on deployments
      const failing =
        b.ref.kind === "agent" &&
        !all.slice(0, i).some((y) => y.ref.kind === "agent");
      const never =
        b.ref.kind === "template" &&
        !all.slice(0, i).some((y) => y.ref.kind === "template");
      const prev =
        failing && head && b.commits.length > 1 ? b.commits.at(-2)! : head;
      const none = !never && b.ref.kind !== "uncommitted" && !prev;
      g.rows[b.gitBranch] = none
        ? {
            pushedSeq: null,
            pushedId: null,
            pushedAt: null,
            gitSha: null,
            error: null,
            attempts: 0,
            notes: [],
          }
        : {
            pushedSeq: never
              ? null
              : b.ref.kind === "uncommitted"
                ? 0
                : (prev?.seq ?? null),
            pushedId: never ? null : (prev?.commitId ?? null),
            pushedAt: never ? null : isoAgo((i + 1) * 3600_000),
            gitSha: never ? null : hex40(`${b.gitBranch}@${prev?.seq ?? 0}`),
            error: failing
              ? {
                  at: isoAgo(4 * 60_000),
                  message:
                    "GitHub 403: Resource not accessible by personal access token (contents: write) — check the token permissions",
                  status: 403,
                }
              : null,
            attempts: failing ? 3 : 0,
            notes:
              b.ref.kind === "deployment" || b.ref.kind === "agent"
                ? SEED_NOTES
                : [],
            ...(failing ? { failNext: true } : {}),
          };
    } else
      g.rows[b.gitBranch] = {
        pushedSeq: null,
        pushedId: null,
        pushedAt: null,
        gitSha: null,
        error: null,
        attempts: 0,
        notes: [],
      };
  }
  g.seeded = true;
}

const headSeq = (b: Branch) =>
  b.drift !== undefined ? b.drift : (b.commits.at(-1)?.seq ?? 0);
function advance(g: MockGithub, b: Branch, r: Row) {
  if (r.syncAt === undefined || Date.now() < r.syncAt) return;
  delete r.syncAt;
  if (r.failNext) {
    r.failNext = false;
    r.attempts += 1;
    r.error = {
      at: iso(Date.now()),
      message:
        "GitHub 403: Resource not accessible by personal access token (contents: write) — check the token permissions",
      status: 403,
    };
    return;
  }
  const head = b.commits.at(-1) ?? null;
  r.pushedSeq = headSeq(b);
  r.pushedId =
    b.drift !== undefined ? `drift-${b.drift}` : (head?.commitId ?? null);
  r.pushedAt = iso(Date.now());
  r.gitSha = hex40(`${b.gitBranch}@${r.pushedSeq}`); // deterministic: a rebuild of the same history = the same sha
  r.error = null;
  r.attempts = 0;
  void g;
}

function rowView(db: MockDb, b: Branch): MirrorBranchState {
  const g = gh(db);
  const r = g.rows[b.gitBranch]!;
  advance(g, b, r);
  const repo =
    g.doc.owner && g.doc.repo
      ? `https://github.com/${g.doc.owner}/${g.doc.repo}`
      : null;
  const hs = headSeq(b);
  const pending =
    r.pushedSeq === null
      ? b.drift !== undefined
        ? 1
        : b.commits.length
      : Math.max(0, hs - r.pushedSeq);
  const paused =
    !!g.doc.pausedUntil && Date.parse(g.doc.pausedUntil) > Date.now();
  const status: MirrorBranchState["status"] =
    r.syncAt !== undefined
      ? "pushing"
      : r.error
        ? "error"
        : pending > 0 && paused
          ? "paused"
          : pending > 0 && r.pushedSeq === null && !g.doc.enabled
            ? "never"
            : pending > 0
              ? "queued"
              : r.pushedSeq === null
                ? "never"
                : "synced";
  const pushed =
    r.pushedSeq !== null && b.drift === undefined
      ? (b.commits.find((c) => c.seq === r.pushedSeq) ?? null)
      : null;
  return {
    gitBranch: b.gitBranch,
    branch: b.ref,
    label: b.label,
    status,
    last:
      r.pushedSeq === null
        ? null
        : b.drift !== undefined
          ? {
              op: "drift",
              opmId: r.pushedId ?? "",
              seq: null,
              message: `Uncommitted changes on ${b.ref.siteSlug} (${b.drift} items) — not committed in OPM`,
              at: r.pushedAt ?? iso(Date.now()),
            }
          : {
              op: "commit",
              opmId: pushed?.commitId ?? r.pushedId ?? "",
              seq: r.pushedSeq,
              message: pushed?.message ?? `commit ${r.pushedSeq}`,
              at: pushed?.createdAt ?? r.pushedAt ?? iso(Date.now()),
            },
    gitSha: r.gitSha,
    gitUrl: repo && r.gitSha ? `${repo}/commit/${r.gitSha}` : null,
    treeUrl: repo && r.gitSha ? `${repo}/tree/${b.gitBranch}` : null,
    pushedAt: r.pushedAt,
    queue: {
      pending:
        r.syncAt !== undefined || status === "pushing" ? pending : pending,
      failed: r.error ? 1 : 0,
      nextAttemptAt: r.error ? iso(Date.parse(r.error.at) + 300_000) : null,
    },
    lastError: r.error,
    notes: r.notes,
    leaseUntil: r.syncAt !== undefined ? iso(r.syncAt) : null,
    updatedAt: r.pushedAt ?? g.doc.updatedAt,
  };
}

function view(db: MockDb): GithubMirrorView {
  const g = gh(db);
  const d = g.doc;
  const rows = branches(db).map((b) => rowView(db, b));
  return {
    configured: !!(d.owner && d.repo && d.token),
    enabled: d.enabled,
    owner: d.owner,
    repo: d.repo,
    repoUrl:
      d.owner && d.repo ? `https://github.com/${d.owner}/${d.repo}` : null,
    token: maskGithubToken(d.token),
    tokenSet: !!d.token,
    tokenSetAt: d.tokenSetAt,
    tokenSetBy: d.tokenSetBy,
    initializedAt: d.initializedAt,
    lastTest: d.lastTest,
    pausedUntil: d.pausedUntil,
    pauseReason: d.pauseReason,
    summary: {
      branches: rows.length,
      synced: rows.filter((r) => r.status === "synced").length,
      queued: rows.filter(
        (r) => r.status === "queued" || r.status === "pushing",
      ).length,
      errors: rows.filter((r) => r.status === "error").length,
      pendingItems: rows.reduce((n, r) => n + r.queue.pending, 0),
      lastPushAt:
        rows
          .map((r) => r.pushedAt)
          .filter((x): x is string => !!x)
          .sort()
          .at(-1) ?? null,
    },
    exportFormat: MIRROR_EXPORT_FORMAT,
    tool: {
      version: "0.15.0-mock",
      amd64: "sha256:" + hex40("amd64") + hex40("amd64").slice(0, 24),
      arm64: "sha256:" + hex40("arm64") + hex40("arm64").slice(0, 24),
    },
    updatedAt: d.updatedAt,
    updatedBy: d.updatedBy,
  };
}

interface Deps {
  err: (
    code: ErrorCode,
    message: string,
    details?: Record<string, unknown>,
  ) => Error;
  invalid: (issues: ValidationIssue[], message?: string) => Error;
  issue: (path: string, code: string, message: string) => ValidationIssue;
  needAdmin: (c: unknown) => void;
}
type C = {
  body: unknown;
  query: URLSearchParams;
  me: { uid: string };
  db: MockDb;
};

export function githubHandlers(x: Deps): Record<string, (c: C) => unknown> {
  const touch = (g: MockGithub, uid: string) => {
    g.doc.updatedAt = iso(Date.now());
    g.doc.updatedBy = uid;
  };
  /** queue every branch that is behind (or one) and start pushing (≈1.5 s) */
  const kick = (db: MockDb, only: string | undefined, all: boolean) => {
    const g = gh(db);
    let enq = 0;
    const list = branches(db).filter((b) => !only || b.gitBranch === only);
    for (const [i, b] of list.entries()) {
      const r = g.rows[b.gitBranch]!;
      const behind =
        r.pushedSeq === null || headSeq(b) > r.pushedSeq || !!r.error;
      if (!all && !behind) continue;
      r.syncAt = Date.now() + 1200 + i * 120;
      if (r.error) r.error = null;
      enq++;
    }
    return { enqueued: enq, branches: list.length, processed: 0 };
  };
  return {
    getGithubMirror: (c) => {
      x.needAdmin(c);
      return view(c.db);
    },
    patchGithubMirror: (c) => {
      x.needAdmin(c);
      const g = gh(c.db);
      const b = (c.body ?? {}) as Record<string, unknown>;
      const out: ValidationIssue[] = [];
      for (const k of Object.keys(b))
        if (!["owner", "repo", "enabled", "token"].includes(k))
          out.push(x.issue(k, "V_UNKNOWN", `unknown field ${k}`));
      if (
        b.owner !== undefined &&
        (typeof b.owner !== "string" ||
          (b.owner !== "" && !GITHUB_OWNER_RE.test(b.owner)))
      )
        out.push(
          x.issue(
            "owner",
            "V_FORMAT",
            'owner: a GitHub user or organization (letters, digits, "-", ≤ 39)',
          ),
        );
      if (
        b.repo !== undefined &&
        (typeof b.repo !== "string" ||
          (b.repo !== "" && !GITHUB_REPO_RE.test(b.repo)))
      )
        out.push(
          x.issue(
            "repo",
            "V_FORMAT",
            'repository: letters, digits, ".", "_", "-" (≤ 100)',
          ),
        );
      if (
        b.token !== undefined &&
        b.token !== null &&
        (typeof b.token !== "string" || !GITHUB_TOKEN_RE.test(b.token))
      )
        out.push(
          x.issue(
            "token",
            "V_FORMAT",
            "token: 20–255 printable characters without spaces (a fine-grained PAT starts with github_pat_)",
          ),
        );
      if (b.enabled !== undefined && typeof b.enabled !== "boolean")
        out.push(x.issue("enabled", "V_TYPE", "enabled: true or false"));
      if (out.length) throw x.invalid(out);
      const next = { ...g.doc };
      if (typeof b.owner === "string") next.owner = b.owner;
      if (typeof b.repo === "string") next.repo = b.repo;
      if (b.token !== undefined) {
        next.token = (b.token as string | null) ?? null;
        next.tokenSetAt = b.token ? iso(Date.now()) : null;
        next.tokenSetBy = b.token ? c.me.uid : null;
      }
      if (typeof b.enabled === "boolean") next.enabled = b.enabled;
      if (!next.token) next.enabled = false;
      if (next.enabled && !(next.owner && next.repo && next.token))
        throw x.invalid([
          x.issue(
            "enabled",
            "V_REQUIRED",
            "enabling needs the repository owner, name and a token",
          ),
        ]);
      const repoChanged =
        next.owner !== g.doc.owner || next.repo !== g.doc.repo;
      const enabling = next.enabled && !g.doc.enabled;
      g.doc = next;
      if (repoChanged) {
        g.doc.lastTest = null;
        g.doc.initializedAt = null;
        for (const r of Object.values(g.rows))
          Object.assign(r, {
            pushedSeq: null,
            pushedId: null,
            pushedAt: null,
            gitSha: null,
            error: null,
            attempts: 0,
          });
      }
      if (b.token) g.doc.pausedUntil = null; // a new token lifts an auth pause
      touch(g, c.me.uid);
      if (g.doc.enabled && (repoChanged || enabling))
        kick(c.db, undefined, false);
      return view(c.db);
    },
    testGithubMirror: (c) => {
      x.needAdmin(c);
      const g = gh(c.db);
      const d = g.doc;
      if (!d.owner || !d.repo || !d.token)
        throw x.invalid([
          x.issue(
            !d.token ? "token" : !d.owner ? "owner" : "repo",
            "V_REQUIRED",
            "set the repository owner, name and a token first",
          ),
        ]);
      const at = iso(Date.now());
      const badTok = /bad$/i.test(d.token);
      const missing = /missing|nope/i.test(d.repo);
      const readonly = /readonly/i.test(d.token);
      const checks: GithubTestResponse["checks"] = [
        {
          name: "token",
          ok: !badTok,
          message: badTok
            ? "GitHub 401 Bad credentials — the token is wrong, expired or revoked"
            : "token accepted (fine-grained personal access token)",
        },
        {
          name: "repo",
          ok: !badTok && !missing,
          message: badTok
            ? "not checked"
            : missing
              ? `GitHub 404: ${d.owner}/${d.repo} does not exist or the token has no access to it`
              : `${d.owner}/${d.repo} found (private)`,
        },
        {
          name: "contents-read",
          ok: !badTok && !missing,
          message:
            badTok || missing
              ? "not checked"
              : "can list branches (Contents: read)",
        },
        {
          name: "contents-write",
          ok: !badTok && !missing && !readonly,
          message:
            badTok || missing
              ? "not checked"
              : readonly
                ? "GitHub 403 writing a test blob — give the token Contents: Read and write"
                : "can write (a test blob was created; unreferenced, GitHub drops it)",
        },
        {
          name: "rate",
          ok: true,
          message: "4 987 of 5 000 requests left this hour",
        },
      ];
      const ok = checks.every((k) => k.ok);
      const res: GithubTestResponse = {
        ok,
        at,
        checks,
        repo:
          badTok || missing
            ? null
            : {
                fullName: `${d.owner}/${d.repo}`,
                private: true,
                defaultBranch: d.initializedAt ? "main" : null,
                empty: !d.initializedAt,
                htmlUrl: `https://github.com/${d.owner}/${d.repo}`,
              },
        login: badTok ? null : "acme-ops-bot",
        rate: {
          limit: 5000,
          remaining: 4987,
          resetAt: iso(Date.now() + 40 * 60_000),
        },
      };
      g.doc.lastTest = res;
      return res;
    },
    revealGithubToken: (c) => {
      x.needAdmin(c);
      const g = gh(c.db);
      if (!g.doc.token) throw x.err("not-found", "no token is stored");
      return { value: g.doc.token, hideAfterSec: REVEAL_SECONDS };
    },
    listGithubMirrorBranches: (c) => {
      x.needAdmin(c);
      return { items: branches(c.db).map((b) => rowView(c.db, b)) };
    },
    listGithubMirrorQueue: (c) => {
      x.needAdmin(c);
      const only = c.query.get("gitBranch");
      const items: MirrorQueueItem[] = [];
      for (const b of branches(c.db)) {
        if (only && b.gitBranch !== only) continue;
        const r = gh(c.db).rows[b.gitBranch]!;
        const from = (r.pushedSeq ?? 0) + 1;
        for (const cm of b.commits.filter((k) => k.seq >= from)) {
          items.push({
            itemId: `${b.gitBranch}__commit__${cm.commitId}`,
            gitBranch: b.gitBranch,
            branch: b.ref,
            op: "commit",
            opmId: cm.commitId,
            seq: cm.seq,
            orderKey: `${cm.createdAt}|${String(cm.seq).padStart(10, "0")}|${cm.commitId}`,
            state: r.error ? "failed" : r.syncAt ? "running" : "pending",
            attempts: r.attempts,
            nextAttemptAt: iso(Date.now()),
            createdAt: cm.createdAt,
            startedAt: null,
            doneAt: null,
            gitSha: null,
            lastError: r.error,
            reason: "commit",
          });
        }
      }
      return { items: items.slice(0, 200) };
    },
    syncGithubMirror: (c) => {
      x.needAdmin(c);
      const g = gh(c.db);
      if (!g.doc.enabled)
        throw x.err("conflict", "the mirror is disabled — enable it first");
      g.doc.pausedUntil = null;
      g.doc.pauseReason = null;
      return kick(
        c.db,
        ((c.body ?? {}) as { gitBranch?: string }).gitBranch,
        false,
      );
    },
    resyncGithubMirror: (c) => {
      x.needAdmin(c);
      const g = gh(c.db);
      if (!g.doc.enabled)
        throw x.err("conflict", "the mirror is disabled — enable it first");
      return kick(
        c.db,
        ((c.body ?? {}) as { gitBranch?: string }).gitBranch,
        true,
      );
    },
  };
}

/** test hook: window.__opmMockGithub('unconfigured' | 'paused' | 'failing' | 'disabled') */
if (typeof window !== "undefined") {
  (
    window as unknown as { __opmMockGithub: (s: string) => void }
  ).__opmMockGithub = (s) => {
    const db = getDb();
    ensureGithub(db);
    const g = gh(db);
    if (s === "unconfigured") {
      g.doc = {
        ...g.doc,
        enabled: false,
        owner: "",
        repo: "",
        token: null,
        tokenSetAt: null,
        tokenSetBy: null,
        initializedAt: null,
        lastTest: null,
        pausedUntil: null,
        pauseReason: null,
      };
      for (const r of Object.values(g.rows))
        Object.assign(r, {
          pushedSeq: null,
          pushedId: null,
          pushedAt: null,
          gitSha: null,
          error: null,
          attempts: 0,
          notes: [],
        });
    } else if (s === "paused") {
      g.doc.pausedUntil = iso(Date.now() + 37 * 60_000);
      g.doc.pauseReason =
        "GitHub rate limit reached (secondary limit on content creation) — pushing resumes automatically";
      const first = Object.values(g.rows).find(
        (r) => r.pushedSeq !== null && !r.error,
      );
      if (first) first.pushedSeq = Math.max(0, (first.pushedSeq ?? 1) - 1);
    } else if (s === "disabled") g.doc.enabled = false;
    saveNow();
  };
}
export type { MirrorBranchKind };
