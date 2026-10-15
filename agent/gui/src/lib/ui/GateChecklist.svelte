<script lang="ts">
  import type { GateCheck } from '@opm/shared/types';
  import Icon from './Icon.svelte';
  let { gate }: { gate: GateCheck } = $props();
  const LABEL: Record<string, string> = { parent: 'Parent = head on every site', reachable: 'Every site reachable', staged: 'Staged on every site', idle: 'No update running', status: 'Proposal is ready' };
  /** §21 #110 warn = ok but worth a look (a running update never closes a gate: starting stops it) */
  const label = (c: GateCheck['conditions'][number]) => (c.key === 'idle' && c.warn ? 'An update is running' : (LABEL[c.key] ?? c.key));
</script>

<ul class="gate" data-testid="gate" aria-label="Gate conditions">
  {#each gate.conditions as c (c.key)}
    {@const warn = c.ok && !!c.warn}
    <li class:ok={c.ok && !warn} class:warn data-testid="gate-{c.key}" data-state={warn ? 'warn' : c.ok ? 'ok' : 'bad'}>
      <span class="ic" aria-label={warn ? 'warning' : c.ok ? 'ok' : 'not ok'}><Icon name={warn ? 'alert' : c.ok ? 'check' : 'x'} size={14} /></span>
      <div class="grow"><strong>{label(c)}</strong><div class="muted small">{c.detail}</div></div>
    </li>
  {/each}
</ul>

<style>
  .gate {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 8px;
  }
  li {
    display: flex;
    gap: 10px;
    align-items: flex-start;
    font-size: 13px;
  }
  .ic {
    width: 22px;
    height: 22px;
    border-radius: 50%;
    display: grid;
    place-items: center;
    background: var(--red-soft);
    color: var(--red-text);
    flex: none;
  }
  .ok .ic {
    background: var(--green-soft);
    color: var(--green-text);
  }
  .warn .ic {
    background: var(--amber-soft);
    color: var(--amber-text);
  }
</style>
