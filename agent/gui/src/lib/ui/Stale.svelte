<script lang="ts">
  import { fmtClock } from '../format';
  let { lastSeenAt }: { lastSeenAt: string | null | undefined } = $props();
</script>

<span class="badge warn" title="This site does not answer; the data shown is the last known state" data-testid="stale">stale · last seen {lastSeenAt ? fmtClock(lastSeenAt) : 'never'}</span>
