/**
 * §21 #118 open a site's agent GUI through the cloud: the window opens at once (popup blockers allow it only inside the
 * click), then openRemoteGui sets the remote session cookie and the window goes to the remote URL.
 */
import { api } from '$lib/api';
import { toast } from '$lib/toast.svelte';

export async function openRemoteGui(deploymentId: string, siteSlug: string): Promise<void> {
  const w = window.open('about:blank', `opm-remote-${deploymentId}-${siteSlug}`);
  try {
    const r = await api.openRemoteGui({ deploymentId, siteSlug });
    if (w) w.location.href = r.url;
    else window.location.href = r.url;
    if (!r.connected) toast.info(`Site ${siteSlug} is connecting`, 'Its agent starts answering within a few seconds (agent 0.19.0 or newer, remote GUI on).');
  } catch (e) {
    w?.close();
    toast.error(e, `Cannot open the agent GUI of ${siteSlug}`);
  }
}
