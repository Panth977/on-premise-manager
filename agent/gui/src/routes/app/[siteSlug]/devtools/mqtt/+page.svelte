<script lang="ts">
  /** Dev tools › MQTT (§21 #97): live subscriptions, topic tree, publish. */
  import DevFrame from '$lib/devtools/DevFrame.svelte';
  import MqttClient from '$lib/devtools/mqtt/MqttClient.svelte';
</script>

<DevFrame tool="mqtt">
  {#snippet workbench(conn, info, site)}
    <MqttClient {conn} {info} {site} />
  {/snippet}
</DevFrame>
