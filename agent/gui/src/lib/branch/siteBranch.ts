/**
 * §21 #111 the branch of ONE site, from any site's GUI: the site of the page (/_opm/app/{site}/branch) when it is not
 * this agent → /_opm/api/sites/{site}/branch/… (relayed over the mesh, the person as actor); this agent (or "*") → the
 * plain /_opm/api/branch/… endpoints (older agents have only those). Commit of the browser's edits and Fast-forward all
 * are not here: they always run on the agent the person is logged in to.
 */
import type { DriftPolicy, LocalCommitRequest, LocalCompareRequest, LocalDiscardRequest, LocalMergeRequest, LocalRevertRequest } from '@opm/shared/branch';
import { api } from '../api';
import { overview } from '../state/app.svelte';
import { session } from '../state/session.svelte';

/** the agent the person is logged in to */
export const selfSite = () => overview.data?.self ?? session.info?.siteSlug ?? '';
/** the site of a page when it is another site than this agent (null = this agent) */
export function remoteSite(site: string | null | undefined): string | null {
  if (!site || site === '*') return null;
  const self = selfSite();
  return self && site !== self ? site : null;
}

export type SiteBranchApi = ReturnType<typeof siteBranchApi>;

export function siteBranchApi(site: string | null | undefined) {
  const r = remoteSite(site);
  if (!r) {
    return {
      remote: null as string | null,
      state: () => api.branchState({}),
      commits: (query: { cursor?: string }) => api.listBranchCommits({ query }),
      commit: (commitId: string) => api.getBranchCommit({ commitId }),
      blob: (sha256: string) => api.getBranchBlob({ sha256 }),
      uncommitted: () => api.getUncommitted({}),
      rescan: () => api.rescanDrift({}),
      driftPolicy: () => api.getDriftPolicy({}),
      setDriftPolicy: (body: DriftPolicy) => api.putDriftPolicy({ body }),
      /** the changes made on the server (drift keys / notes) */
      commitDrift: (body: LocalCommitRequest) => api.commitLocal({ body }),
      discard: (body: LocalDiscardRequest) => api.discardLocal({ body }),
      compare: (body: LocalCompareRequest) => api.compareLocal({ body }),
      preview: (body: LocalMergeRequest) => api.previewLocalMerge({ body }),
      merge: (body: LocalMergeRequest) => api.mergeLocal({ body }),
      revert: (body: LocalRevertRequest) => api.revertLocal({ body }),
      reset: (body: LocalRevertRequest) => api.resetLocal({ body }),
      currentUpdate: () => api.currentUpdate({}),
      getUpdate: (updateId: string) => api.getUpdate({ updateId }),
      abandon: (updateId: string) => api.abandonUpdate({ updateId }),
    };
  }
  const siteSlug = r;
  return {
    remote: siteSlug as string | null,
    state: () => api.siteBranchState({ siteSlug }),
    commits: (query: { cursor?: string }) => api.siteListBranchCommits({ siteSlug, query }),
    commit: (commitId: string) => api.siteGetBranchCommit({ siteSlug, commitId }),
    blob: (sha256: string) => api.siteGetBranchBlob({ siteSlug, sha256 }),
    uncommitted: () => api.siteGetUncommitted({ siteSlug }),
    rescan: () => api.siteRescanDrift({ siteSlug }),
    driftPolicy: () => api.siteGetDriftPolicy({ siteSlug }),
    setDriftPolicy: (body: DriftPolicy) => api.sitePutDriftPolicy({ siteSlug, body }),
    commitDrift: (body: LocalCommitRequest) => api.siteCommitDrift({ siteSlug, body }),
    discard: (body: LocalDiscardRequest) => api.siteDiscardLocal({ siteSlug, body }),
    compare: (body: LocalCompareRequest) => api.siteCompareLocal({ siteSlug, body }),
    preview: (body: LocalMergeRequest) => api.sitePreviewLocalMerge({ siteSlug, body }),
    merge: (body: LocalMergeRequest) => api.siteMergeLocal({ siteSlug, body }),
    revert: (body: LocalRevertRequest) => api.siteRevertLocal({ siteSlug, body }),
    reset: (body: LocalRevertRequest) => api.siteResetLocal({ siteSlug, body }),
    currentUpdate: () => api.siteCurrentUpdate({ siteSlug }),
    getUpdate: (updateId: string) => api.siteGetUpdate({ siteSlug, updateId }),
    abandon: (updateId: string) => api.siteAbandonUpdate({ siteSlug, updateId }),
  };
}
