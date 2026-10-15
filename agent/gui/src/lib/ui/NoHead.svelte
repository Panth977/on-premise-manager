<script lang="ts">
  import Empty from './Empty.svelte';
  import CloudNextStep from './CloudNextStep.svelte';
</script>

<Empty icon="layers" title="No configuration yet" text="This site has no head revision (fresh install), so there is nothing to edit locally yet. The first configuration comes from the cloud:">
  {#snippet detail()}<CloudNextStep updatesLink />{/snippet}
</Empty>
