<script lang="ts">
  /**
   * §21 #96 data-directory mismatch helper (Status page): a container that keeps restarting and mounts a volume, whose
   * log shows the custom-pg banner "DATA DIRECTORY / IMAGE VERSION MISMATCH" or PostgreSQL's "database files are
   * incompatible with server" → a hint with one click to the guided "Copy data volume aside, then empty it".
   */
  import type { ContainerStatus, SiteVolumesResponse } from '@opm/shared/types';
  import { dataDirMismatch, type DataDirMismatch } from '@opm/shared/volume-ops';
  import { api } from '$lib/api';
  import Icon from '$lib/ui/Icon.svelte';
  import Btn from '$lib/ui/Btn.svelte';
  import AsideEmptyDrawer from './AsideEmptyDrawer.svelte';

  let { siteSlug, containers, onchanged }: { siteSlug: string; containers: ContainerStatus[]; onchanged?: () => void } = $props();
  let vols = $state<SiteVolumesResponse | null>(null);
  let hits = $state<{ container: string; m: DataDirMismatch }[]>([]);
  let open = $state(false);
  let cur = $state<{ container: string; m: DataDirMismatch } | null>(null);
  const suspects = $derived(containers.filter((c) => c.managed && (c.state === 'restarting' || ((c.state === 'exited' || c.state === 'dead') && c.enabled) || (c.restarts > 0 && !c.ready))).map((c) => c.containerSlug));
  const key = $derived(suspects.join(','));
  let checked = '';
  $effect(() => {
    const k = key;
    const site = siteSlug;
    if (!k || k === checked) {
      if (!k) hits = [];
      return;
    }
    checked = k;
    void (async () => {
      try {
        const v = await api.siteVolumes({ siteSlug: site });
        vols = v;
        const mounted = new Set(v.volumes.flatMap((x) => x.usedBy.map((u) => u.containerSlug)));
        const out: typeof hits = [];
        for (const c of k.split(',')) {
          if (!mounted.has(c)) continue;
          const logs = await api.containerLogs({ siteSlug: site, containerSlug: c, query: { tail: 300 } });
          const m = dataDirMismatch(logs.lines.map((l) => l.text).join('\n'));
          if (m) out.push({ container: c, m });
        }
        hits = out;
      } catch {
        /* the hint is optional */
      }
    })();
  });
  const volsOf = (c: string) =>
    (vols?.volumes ?? [])
      .filter((v) => v.declared && v.usedBy.some((u) => u.containerSlug === c))
      .map((v) => ({ volumeSlug: v.volumeSlug, kind: v.kind, users: [...new Set(v.usedBy.map((u) => u.containerSlug))], mountPath: v.usedBy.find((u) => u.containerSlug === c)?.mountPath ?? '' }));
</script>

{#each hits as h (h.container)}
  <div class="banner warn mm" data-testid="mismatch-{h.container}">
    <Icon name="alert" />
    <div class="grow">
      <strong>{h.container}</strong> keeps restarting: its data directory was made by another {h.m.kind === 'custom-pg' ? 'custom-pg image version' : 'PostgreSQL major version'}.
      <div class="mono small ln">{h.m.line}</div>
      <div class="small">Copy the data volume aside (kept on the server), then empty it — the container initialises a new data directory. You can restore or migrate from the copy later.</div>
    </div>
    <Btn variant="primary" icon="copy" onclick={() => ((cur = h), (open = true))} data-testid="mismatch-fix-{h.container}">Copy data volume aside, then empty it</Btn>
  </div>
{/each}
{#if cur}
  <AsideEmptyDrawer bind:open {siteSlug} container={cur.container} volumes={volsOf(cur.container)} reason={cur.m.line} ondone={() => ((checked = ''), onchanged?.())} />
{/if}

<style>
  .mm {
    margin-bottom: 14px;
    align-items: center;
    flex-wrap: wrap;
  }
  .ln {
    margin: 4px 0;
    overflow-wrap: anywhere;
  }
</style>
