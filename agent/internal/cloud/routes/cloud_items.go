package routes

import "opm/agent/internal/cloud/core"

// The cloud view (routes/agent.go) lists the sites with an open remote GUI session and the port tunnels.
func init() {
	RemoteGuiCloudItems = core.RemoteActiveSites
	PortTunnelCloudItems = portTunnelCloudItems
}
