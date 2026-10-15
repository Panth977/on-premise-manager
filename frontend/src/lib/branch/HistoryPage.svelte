<script lang="ts">
  import { untrack } from 'svelte';
  /** §21 #104 History page of a template / deployment: the shared HistoryView (list, open, compare two, revert, reset). */
  import { branchKey } from '@opm/shared';
  import HistoryView from '$opm/branch/HistoryView.svelte';
  import { setBranchHost } from '$opm/branch/host';
  import { createCloudBranchHost, rememberLabel } from './branchHost';
  import type { AnyScope } from '../config-editor/scope.svelte';
  import PageHeader from '../ui/PageHeader.svelte';
  import LegacyProposals from './LegacyProposals.svelte';
  import { toast } from '../toast.svelte';

  let { scope }: { scope: AnyScope } = $props();
  const sc = untrack(() => scope);
  rememberLabel(sc.branchRef, sc.label);
  const canWrite = sc.kind === 'template' ? sc.canWrite : sc.can('deployment.config', 'publish');
  let version = $state(0);
  setBranchHost(
    createCloudBranchHost({
      target: sc.branchRef,
      label: (b) => (branchKey(b) === branchKey(sc.branchRef) ? sc.label : branchKey(b)),
      canWrite,
      canSecrets: sc.kind === 'deployment' ? { read: sc.can('deployment.secrets', 'read'), write: sc.can('deployment.secrets', 'write') } : undefined,
      deploymentId: sc.kind === 'deployment' ? sc.id : null,
      changed: () => {
        void sc.refresh();
        version++;
      },
    }),
  );
</script>

<PageHeader title="History" subtitle="Every commit of {scope.label}, newest first. Open one to see what it changed; pick two to compare; revert or reset make a NEW commit (history is never rewritten)." />
<HistoryView branch={scope.branchRef} label={scope.label} {version} onchanged={(r) => toast.success(`Commit ${'seq' in r ? r.seq : ''} recorded`)} />
{#if scope.kind === 'deployment'}<LegacyProposals deploymentId={scope.id} />{/if}
