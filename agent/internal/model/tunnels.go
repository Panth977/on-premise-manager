package model

// §21 #125 port tunnels (TS shared/src/tunnels.ts).

const (
	PortTunnelChunkMax = 1024 * 1024
)

// PortTunnelTarget is TS PortTunnelTarget (kind container | edge | host).
type PortTunnelTarget struct {
	Kind          string `json:"kind"`
	ContainerSlug string `json:"containerSlug,omitempty"`
	PortName      string `json:"portName,omitempty"`
	Scheme        string `json:"scheme,omitempty"`
	Port          int    `json:"port,omitempty"`
}

// PortTunnelCloudItem is TS PortTunnelCloudItem.
type PortTunnelCloudItem struct {
	TunnelID   string           `json:"tunnelId"`
	SiteSlug   string           `json:"siteSlug"`
	Target     PortTunnelTarget `json:"target"`
	PublicPort int              `json:"publicPort"`
	Label      *string          `json:"label"`
	CreatedAt  string           `json:"createdAt"`
	CreatedBy  string           `json:"createdBy"`
}

// PortTunnelOpen is TS PortTunnelOpen.
type PortTunnelOpen struct {
	ConnID   string           `json:"connId"`
	TunnelID string           `json:"tunnelId"`
	SiteSlug string           `json:"siteSlug"`
	Target   PortTunnelTarget `json:"target"`
}

// PortTunnelNextResponse is TS PortTunnelNextResponse.
type PortTunnelNextResponse struct {
	Opens []PortTunnelOpen `json:"opens"`
}

// PortTunnelRecvResponse is TS PortTunnelRecvResponse.
type PortTunnelRecvResponse struct {
	DataB64 string `json:"dataB64"`
	Closed  bool   `json:"closed"`
}

// PortTunnelSendRequest is TS PortTunnelSendRequest.
type PortTunnelSendRequest struct {
	DataB64 string  `json:"dataB64"`
	Close   bool    `json:"close,omitempty"`
	Error   *string `json:"error,omitempty"`
}

// SitePortTunnel is TS SitePortTunnel.
type SitePortTunnel struct {
	PortTunnelCloudItem
	Enabled          bool    `json:"enabled"`
	OpenConnections  int     `json:"openConnections"`
	LastConnectionAt *string `json:"lastConnectionAt"`
	LastError        *string `json:"lastError"`
}

// SitePortTunnelsResponse is TS SitePortTunnelsResponse.
type SitePortTunnelsResponse struct {
	SiteSlug     string           `json:"siteSlug"`
	CloudTunnels bool             `json:"cloudTunnels"`
	RemoteGui    bool             `json:"remoteGui"`
	Items        []SitePortTunnel `json:"items"`
}

// SetPortTunnelRequest is TS SetPortTunnelRequest.
type SetPortTunnelRequest struct {
	Enabled *bool `json:"enabled"`
}
