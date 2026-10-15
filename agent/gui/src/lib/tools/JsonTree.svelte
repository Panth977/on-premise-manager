<script lang="ts">
  /** Collapsible JSON viewer (objects / arrays fold; the first `open` levels start expanded). */
  import JsonTree from './JsonTree.svelte';
  let { value, name = null, open = 1, depth = 0 }: { value: unknown; name?: string | null; open?: number; depth?: number } = $props();
  const isObj = $derived(value !== null && typeof value === 'object');
  const entries = $derived(isObj ? (Array.isArray(value) ? value.map((v, i) => [String(i), v] as const) : Object.entries(value as Record<string, unknown>)) : []);
  let expanded = $state(false);
  $effect.pre(() => {
    expanded = depth < open;
  });
  const kind = $derived(value === null ? 'null' : typeof value);
</script>

{#if isObj}
  <div class="jt">
    <button class="tog" onclick={() => (expanded = !expanded)} aria-expanded={expanded}>
      <span class="car" class:on={expanded}>▸</span>{#if name !== null}<span class="k">{name}</span><span class="p">: </span>{/if}<span class="p">{Array.isArray(value) ? '[' : '{'}</span>{#if !expanded}<span class="sum">{entries.length} {Array.isArray(value) ? 'items' : 'keys'}</span><span class="p">{Array.isArray(value) ? ']' : '}'}</span>{/if}
    </button>
    {#if expanded}
      <div class="kids">
        {#each entries as [k, v] (k)}<JsonTree value={v} name={Array.isArray(value) ? null : k} {open} depth={depth + 1} />{/each}
      </div>
      <span class="p">{Array.isArray(value) ? ']' : '}'}</span>
    {/if}
  </div>
{:else}
  <div class="jt leaf">{#if name !== null}<span class="k">{name}</span><span class="p">: </span>{/if}<span class="v {kind}" class:masked={value === '••••••' || (typeof value === 'string' && value.endsWith('=••••••'))}>{kind === 'string' ? JSON.stringify(value) : String(value)}</span></div>
{/if}

<style>
  .jt {
    font-family: var(--font-mono);
    font-size: 12px;
    line-height: 1.6;
  }
  .leaf {
    padding-left: 14px;
    overflow-wrap: anywhere;
  }
  .tog {
    all: unset;
    cursor: pointer;
    display: inline;
  }
  .tog:focus-visible {
    outline: 2px solid var(--accent);
  }
  .car {
    display: inline-block;
    width: 14px;
    color: var(--text-3);
    transition: transform var(--dur-fast);
  }
  .car.on {
    transform: rotate(90deg);
  }
  .kids {
    padding-left: 14px;
    border-left: 1px dashed var(--border);
    margin-left: 5px;
  }
  .k {
    color: var(--violet-text);
  }
  .p,
  .sum {
    color: var(--text-3);
  }
  .sum {
    font-style: italic;
    margin: 0 4px;
  }
  .v.string {
    color: var(--green-text);
  }
  .v.number {
    color: var(--blue-text);
  }
  .v.boolean,
  .v.null {
    color: var(--amber-text);
  }
  .v.masked {
    background: var(--amber-soft);
    border-radius: 3px;
    padding: 0 3px;
  }
</style>
