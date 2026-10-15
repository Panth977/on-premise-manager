<script lang="ts">
  import Icon from '../ui/Icon.svelte';
  import { Cloud } from '../ui/icons';
  /** Shown on every deployment config page (§17.3; §21 #105: changes stay in this browser until Commit). */
  let { deploymentId }: { deploymentId: string } = $props();
</script>

<div class="banner info draft" data-testid="draft-banner">
  <Icon icon={Cloud} size={16} class="icon" />
  <div>
    <b>Changes stay in this browser</b> until you <b>Commit</b> them with a message — a commit is a release (<a href="/app/deployments/{deploymentId}/compare?review=working">review the changes not committed</a>); the sites change only after a person there merges it in the agent GUI.
  </div>
</div>

<style>
  .draft {
    margin-bottom: var(--sp-4);
  }
</style>
