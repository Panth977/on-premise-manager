<script lang="ts">
  /** A RESP reply rendered by type (§21 #97): strings quoted (JSON pretty when it is JSON), integers / doubles, nil, nested arrays, maps as key ⇒ value, sets. */
  import type { RedisReply } from '@opm/shared/devtools';
  import Self from './ReplyView.svelte';
  import { isTrunc } from '../grid';
  import { isJsonText } from './redis';
  let { reply }: { reply: RedisReply } = $props();
  const list = $derived((Array.isArray(reply.value) ? reply.value : []) as RedisReply[]);
  const str = $derived(typeof reply.value === 'string' ? reply.value : isTrunc(reply.value) ? reply.value.preview : reply.value === null ? '' : JSON.stringify(reply.value));
</script>

{#if reply.type === 'nil'}
  <span class="nil">(nil)</span>
{:else if reply.type === 'integer'}
  <span class="num">(integer) {reply.value}</span>
{:else if reply.type === 'double'}
  <span class="num">(double) {reply.value}</span>
{:else if reply.type === 'bool'}
  <span class="num">(boolean) {String(reply.value)}</span>
{:else if reply.type === 'error'}
  <span class="err">(error) {str}</span>
{:else if reply.type === 'status'}
  <span class="st">{str}</span>
{:else if reply.type === 'array' || reply.type === 'set' || reply.type === 'map'}
  {#if !list.length}
    <span class="nil">{reply.type === 'map' ? '(empty map)' : reply.type === 'set' ? '(empty set)' : '(empty array)'}</span>
  {:else if reply.type === 'map'}
    <ol class="lst">
      {#each Array.from({ length: Math.floor(list.length / 2) }, (_, i) => i) as i (i)}
        <li><span class="k"><Self reply={list[i * 2]!} /></span> <span class="arrow">⇒</span> <Self reply={list[i * 2 + 1]!} /></li>
      {/each}
    </ol>
  {:else}
    <ol class="lst">
      {#each list as r, i (i)}<li><Self reply={r} /></li>{/each}
    </ol>
  {/if}
{:else if isJsonText(str) && str.length < 20000}
  <pre class="json">{JSON.stringify(JSON.parse(str), null, 2)}</pre>
{:else}
  <span class="s">{str.includes('\n') ? str : JSON.stringify(str)}</span>{#if isTrunc(reply.value)}<span class="nil"> … cut at {reply.value.sizeBytes} bytes{reply.value.fetchable === false ? ' — use the key browser for the full value' : ''}</span>{/if}
{/if}

<style>
  .nil {
    color: #9696a2;
  }
  .num {
    color: #93c5fd;
  }
  .err {
    color: #f87171;
  }
  .st {
    color: #86efac;
  }
  .s {
    color: #fde68a;
    white-space: pre-wrap;
    word-break: break-word;
  }
  .json {
    margin: 0;
    color: #fde68a;
    white-space: pre-wrap;
    font: inherit;
  }
  .lst {
    margin: 0;
    padding-left: 26px;
  }
  .lst li::marker {
    color: #71717a;
  }
  .k {
    color: #d8b4fe;
  }
  .arrow {
    color: #71717a;
  }
</style>
