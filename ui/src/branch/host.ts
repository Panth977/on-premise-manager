/**
 * Branches + commits (SPEC §21 #104, docs-v2/branches-commits.md §10.1): the ONE adapter between the shared branch
 * components and an app. The cloud console builds it per page (template / deployment / agent-branch target), the agent
 * GUI from its own branch API. Components read it from Svelte context (`useBranchHost()`).
 *
 * Components (all in ui/src/branch/, Svelte 5):
 *   <ChangeCount count={n} />                                          badge "N uncommitted"
 *   <CommitDialog open bind:open … />                                  modal (native <dialog>, top layer): message + changes
 *     props: open (bindable), title?, primaryLabel? ('Commit'), items? (MergeItem[] shown grouped), issues?
 *     (ValidationIssue[]: errors block), warnings? (string[]: needs-agent etc.), runtime? ({containerSlug, action}[]),
 *     busy?, onsubmit(message) → Promise<void>, onclose?(), suggestPrefix?, testid? ('commit-dialog')
 *   <MergeView source target … />                                      compare + cherry-pick page body
 *     props: plan (MergePlan | null; when null and `request` is given the view calls host.compare(request)),
 *     request? (CompareRequest), diffOnly? (default plan.diffOnly), onmerged?(commit | run), sourcePicker? (snippet)
 *   <HistoryView />                                                    commit list + detail + compare two + revert / reset
 *     props: branch (BranchRef), label (string), onchanged?(result) — after a revert / reset
 *   <TextDiff before after />                                          line diff of two texts (files / content)
 *
 * The app provides (setBranchHost(h) in a layout / page):
 *   compare(req)          → MergePlan                (cloud: POST /v1/branches/compare; agent: POST /_opm/api/branch/compare)
 *   preview(req)          → MergePreview             (dry run; debounced by MergeView)
 *   merge(req & message)  → Commit | UpdateRun       (agent returns the run: the commit is written at COMMIT)
 *   commits(cursor)       → PageResponse<CommitSummary>   (of `branch` passed to HistoryView)
 *   commit(id)            → CommitDetail
 *   commitWorking(msg)    → Commit | UpdateRun | null     (change bar "Commit…": §21 #105 commits the BROWSER change set)
 *
 * §21 #105 browser change sets (both apps) — §21 #107: stored in IndexedDB by `drafts.ts` (config + bytes, per user, 1 h
 *   expiry after the app was last open, heartbeat while open, valid only on the current head), bytes in memory by
 *   `pending-blobs.ts`, one Commit = stage + commit by `stage-commit.ts` (`pending.ts` = deprecated localStorage I/O),
 *   <PendingBanner state count savedAt … />   restore / stale banner of a stored set (Restore · Review & rebase · Discard)
 *   <RebaseDialog open conflicts clean onapply />  conflicts of `rebaseChanges` (Keep the head · Take mine · Custom)
 *   ChangeBar (primitives): "N changes (not committed)" · Discard · Review · Commit… — no Save button.
 *   revert / reset        → Commit | UpdateRun
 *   revertPlan / resetPlan(id) → MergeItem[]        (the plan shown in the dialog)
 *   blobText(sha256)      → string | null            (null = binary / too large)
 *   canWrite, labelOf(branch), actor(), now()?, sourceOptions()?
 * Test ids: see the report in ORCH.md (§21 #104) and each component's markup.
 */
import { getContext, setContext } from 'svelte';
import type { PageResponse, UpdateRun } from '@opm/shared/types';
import type {
  BranchPoint,
  BranchRef,
  Commit,
  CommitDetail,
  CommitSummary,
  CompareRequest,
  MergeChoice,
  MergeItem,
  MergePlan,
  MergePreview,
  MergeRequest,
} from '@opm/shared/branch';

export type MergeResult = Commit | UpdateRun;
export interface SourceOption {
  /** pointParam (branchKey[@commitId]) */
  value: string;
  label: string;
  group?: string;
  hint?: string;
}

export interface BranchHost {
  /** 'template' | 'deployment' | 'agent' — wording ("Merge & apply" on the agent) */
  scope: 'template' | 'deployment' | 'agent';
  compare(req: CompareRequest): Promise<MergePlan>;
  preview(req: MergeRequest): Promise<MergePreview>;
  merge(req: MergeRequest & { message: string }): Promise<MergeResult>;
  commits(branch: BranchRef, cursor: string | null): Promise<PageResponse<CommitSummary>>;
  commit(branch: BranchRef, commitId: string): Promise<CommitDetail>;
  commitWorking?(message: string): Promise<MergeResult | null>;
  revertPlan?(branch: BranchRef, commitId: string): Promise<MergeItem[]>;
  revert?(branch: BranchRef, commitId: string, message: string, ifHead: string | null, choices: MergeChoice[]): Promise<MergeResult>;
  resetPlan?(branch: BranchRef, commitId: string): Promise<MergeItem[]>;
  reset?(branch: BranchRef, commitId: string, message: string, ifHead: string | null): Promise<MergeResult>;
  /** text of a blob (files / volume content); null = binary, missing or too large */
  blobText?(sha256: string): Promise<string | null>;
  canWrite: boolean;
  labelOf(branch: BranchRef): string;
  /** the person committing ("you@example.com" / declared actor) */
  actor(): string;
  /** source picker options (Compare page) */
  sourceOptions?(): Promise<SourceOption[]>;
  /** called after a successful merge / commit / revert / reset (apps refresh their state) */
  changed?(r: MergeResult | null): void;
}

const KEY = Symbol('opm-branch-host');
export function setBranchHost(h: BranchHost): BranchHost {
  return setContext(KEY, h);
}
export function useBranchHost(): BranchHost {
  const h = getContext<BranchHost | undefined>(KEY);
  if (!h) throw new Error('branch component used outside a BranchHost context (setBranchHost)');
  return h;
}
export const pointOf = (branch: BranchRef, commitId?: string | null): BranchPoint => ({ branch, at: commitId ? { commitId } : 'head' });
export const isRun = (r: MergeResult | null | undefined): r is UpdateRun => !!r && 'updateId' in r && 'sites' in r;
