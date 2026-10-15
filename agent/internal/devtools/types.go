package devtools

// API shapes of shared/src/devtools.ts (exact JSON keys).

import "opm/agent/internal/model"

// Connection is TS DevToolConnection.
type Connection struct {
	ID             string               `json:"id"`
	SiteSlug       string               `json:"siteSlug"`
	Source         string               `json:"source"` // preset | manual
	PresetRevSeq   *int                 `json:"presetRevSeq"`
	Name           string               `json:"name"`
	Description    string               `json:"description"`
	Kind           string               `json:"kind"`
	Target         model.DevToolTarget  `json:"target"`
	Auth           model.DevToolAuth    `json:"auth"`
	Options        model.DevToolOptions `json:"options"`
	ReadOnly       bool                 `json:"readOnly"`
	CreatedBy      *string              `json:"createdBy"`
	CreatedAt      *string              `json:"createdAt"`
	UpdatedAt      *string              `json:"updatedAt"`
	ResolvedTarget *string              `json:"resolvedTarget"`
	State          ConnState            `json:"state"`
	Problem        *string              `json:"problem"`
}

// ConnState is TS DevToolConnState.
type ConnState struct {
	Status         string  `json:"status"` // idle | connected | error
	ConnectedAt    *string `json:"connectedAt"`
	LastUsedAt     *string `json:"lastUsedAt"`
	LastError      *string `json:"lastError"`
	ServerVersion  *string `json:"serverVersion"`
	RunningQueries int     `json:"runningQueries"`
}

// ConnectionInput is TS DevToolConnectionInput.
type ConnectionInput struct {
	Name        string               `json:"name"`
	Description string               `json:"description"`
	Kind        string               `json:"kind"`
	Target      model.DevToolTarget  `json:"target"`
	Auth        model.DevToolAuth    `json:"auth"`
	Options     model.DevToolOptions `json:"options"`
	ReadOnly    bool                 `json:"readOnly"`
}

// TestConnectionResponse is TS TestConnectionResponse.
type TestConnectionResponse struct {
	OK             bool    `json:"ok"`
	LatencyMs      int64   `json:"latencyMs"`
	ServerVersion  *string `json:"serverVersion"`
	ResolvedTarget *string `json:"resolvedTarget"`
	Message        string  `json:"message"`
}

// InfoContainer is one container of DevToolsInfo.containers.
type InfoContainer struct {
	ContainerSlug string      `json:"containerSlug"`
	ServiceSlug   string      `json:"serviceSlug"`
	Image         string      `json:"image"`
	Running       bool        `json:"running"`
	Ports         []InfoPort  `json:"ports"`
	Mounts        []InfoMount `json:"mounts"`
	// Env (§21 #99): env names (image env + container env) + secret flag, for containers.<c>.env.<NAME> completion.
	Env []InfoEnv `json:"env"`
}

// InfoEnv is one env name of an InfoContainer (never a value).
type InfoEnv struct {
	Name   string `json:"name"`
	Secret bool   `json:"secret"`
}

// InfoPort is one port of an InfoContainer.
type InfoPort struct {
	PortName      string `json:"portName"`
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
	HostPort      *int   `json:"hostPort"`
}

// InfoMount is one mount of an InfoContainer.
type InfoMount struct {
	MountPath  string  `json:"mountPath"`
	Kind       string  `json:"kind"`
	VolumeSlug *string `json:"volumeSlug"`
}

// InfoVolume is one volume of DevToolsInfo.volumes.
type InfoVolume struct {
	VolumeSlug string `json:"volumeSlug"`
	Label      string `json:"label"`
	Kind       string `json:"kind"`
}

// Info is TS DevToolsInfo.
type Info struct {
	SiteSlug   string          `json:"siteSlug"`
	Enabled    bool            `json:"enabled"`
	Reason     *string         `json:"reason"`
	ConfigPath *string         `json:"configPath"`
	Limits     Limits          `json:"limits"`
	Kinds      []string        `json:"kinds"`
	Containers []InfoContainer `json:"containers"`
	Volumes    []InfoVolume    `json:"volumes"`
	Secrets    []string        `json:"secrets"`
	Variables  []string        `json:"variables"`
}

// ───────────────────────── schema

// SchemaColumn is TS SchemaColumn.
type SchemaColumn struct {
	Name       string  `json:"name"`
	Type       string  `json:"type"`
	Nullable   bool    `json:"nullable"`
	Default    *string `json:"default"`
	PrimaryKey bool    `json:"primaryKey"`
	Role       *string `json:"role,omitempty"`
	Comment    *string `json:"comment,omitempty"`
}

// SchemaIndex is TS SchemaIndex.
type SchemaIndex struct {
	Name       string `json:"name"`
	Definition string `json:"definition"`
	Unique     bool   `json:"unique"`
	Primary    bool   `json:"primary"`
}

// SchemaTable is TS SchemaTable.
type SchemaTable struct {
	Name        string         `json:"name"`
	Kind        string         `json:"kind"`
	RowEstimate *float64       `json:"rowEstimate"`
	SizeBytes   *int64         `json:"sizeBytes"`
	Columns     []SchemaColumn `json:"columns"`
	Comment     *string        `json:"comment"`
}

// SchemaFunction is TS SchemaFunction.
type SchemaFunction struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Arguments string `json:"arguments"`
	Returns   string `json:"returns"`
}

// SchemaNamespace is TS SchemaNamespace.
type SchemaNamespace struct {
	Name      string            `json:"name"`
	System    bool              `json:"system"`
	Tables    []SchemaTable     `json:"tables"`
	Functions []SchemaFunction  `json:"functions"`
	Meta      map[string]string `json:"meta"`
}

// SchemaResponse is TS SqlSchemaResponse.
type SchemaResponse struct {
	Kind          string            `json:"kind"`
	ServerVersion *string           `json:"serverVersion"`
	Database      *string           `json:"database"`
	Namespaces    []SchemaNamespace `json:"namespaces"`
	LoadedAt      string            `json:"loadedAt"`
	Truncated     bool              `json:"truncated"`
}

// Constraint is one TableDetailResponse.constraints item.
type Constraint struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Definition string `json:"definition"`
}

// TableDetailResponse is TS TableDetailResponse.
type TableDetailResponse struct {
	Namespace   string         `json:"namespace"`
	Table       string         `json:"table"`
	Columns     []SchemaColumn `json:"columns"`
	Indexes     []SchemaIndex  `json:"indexes"`
	Constraints []Constraint   `json:"constraints"`
	DDL         *string        `json:"ddl"`
	RowEstimate *float64       `json:"rowEstimate"`
	SizeBytes   *int64         `json:"sizeBytes"`
}

// ───────────────────────── execute

// CellTruncated is TS CellTruncated.
type CellTruncated struct {
	Truncated bool   `json:"$truncated"`
	Preview   string `json:"preview"`
	SizeBytes int    `json:"sizeBytes"`
	Row       int    `json:"row"`
	Col       int    `json:"col"`
	// Fetchable false = not fetchable by resultId (Redis replies / key values); omitted elsewhere
	Fetchable *bool `json:"fetchable,omitempty"`
}

// ResultColumn is TS ResultColumn.
type ResultColumn struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Display string `json:"display"`
}

// StatementError is StatementResult.error.
type StatementError struct {
	Message  string  `json:"message"`
	Code     *string `json:"code"`
	Position *int    `json:"position"`
	Detail   *string `json:"detail"`
	Hint     *string `json:"hint"`
}

// Plan is StatementResult.plan.
type Plan struct {
	Text string `json:"text"`
	JSON any    `json:"json"`
}

// StatementResult is TS StatementResult.
type StatementResult struct {
	Index        int             `json:"index"`
	Statement    string          `json:"statement"`
	OK           bool            `json:"ok"`
	Error        *StatementError `json:"error"`
	Type         string          `json:"type"`
	CommandTag   *string         `json:"commandTag"`
	RowsAffected *int64          `json:"rowsAffected"`
	Columns      []ResultColumn  `json:"columns"`
	Rows         [][]any         `json:"rows"`
	TotalRows    int             `json:"totalRows"`
	Truncated    bool            `json:"truncated"`
	ResultID     *string         `json:"resultId"`
	DurationMs   int64           `json:"durationMs"`
	Plan         *Plan           `json:"plan,omitempty"`
	Notices      []string        `json:"notices,omitempty"`
}

// ExecuteSQLRequest is TS ExecuteSqlRequest.
type ExecuteSQLRequest struct {
	Statement   string  `json:"statement"`
	Params      []any   `json:"params"`
	Explain     *string `json:"explain"`
	QueryID     string  `json:"queryId"`
	TimeoutSec  *int    `json:"timeoutSec"`
	MaxRows     *int    `json:"maxRows"`
	Confirm     string  `json:"confirm"`
	Transaction bool    `json:"transaction"`
	Consistency *string `json:"consistency"`
}

// ExecuteResponse is TS ExecuteResponse.
type ExecuteResponse struct {
	QueryID      string            `json:"queryId"`
	ConnectionID string            `json:"connectionId"`
	OK           bool              `json:"ok"`
	DurationMs   int64             `json:"durationMs"`
	Results      []StatementResult `json:"results"`
	ActivityID   *string           `json:"activityId"`
}

// ResultPageResponse is TS ResultPageResponse.
type ResultPageResponse struct {
	ResultID  string  `json:"resultId"`
	Offset    int     `json:"offset"`
	Rows      [][]any `json:"rows"`
	TotalRows int     `json:"totalRows"`
	Truncated bool    `json:"truncated"`
}

// ExecuteRedisRequest is TS ExecuteRedisRequest.
type ExecuteRedisRequest struct {
	Command    []string `json:"command"`
	Line       *string  `json:"line"`
	QueryID    string   `json:"queryId"`
	TimeoutSec *int     `json:"timeoutSec"`
	Confirm    string   `json:"confirm"`
}

// RedisReply is TS RedisReply.
type RedisReply struct {
	Type  string `json:"type"`
	Value any    `json:"value"`
}

// ExecuteRedisResponse is TS ExecuteRedisResponse.
type ExecuteRedisResponse struct {
	QueryID    string      `json:"queryId"`
	OK         bool        `json:"ok"`
	DurationMs int64       `json:"durationMs"`
	Command    string      `json:"command"`
	Reply      *RedisReply `json:"reply"`
	Error      *string     `json:"error"`
	ActivityID *string     `json:"activityId"`
}

// RedisKeyRow is TS RedisKeyRow.
type RedisKeyRow struct {
	Key       string `json:"key"`
	Type      string `json:"type"`
	TTLSec    int64  `json:"ttlSec"`
	SizeBytes *int64 `json:"sizeBytes"`
	Length    *int64 `json:"length"`
}

// RedisScanResponse is TS RedisScanResponse.
type RedisScanResponse struct {
	Cursor  string        `json:"cursor"`
	Keys    []RedisKeyRow `json:"keys"`
	Scanned int           `json:"scanned"`
	DBSize  int64         `json:"dbSize"`
}

// RedisKeyValueResponse is TS RedisKeyValueResponse.
type RedisKeyValueResponse struct {
	Key       string  `json:"key"`
	Type      string  `json:"type"`
	TTLSec    int64   `json:"ttlSec"`
	Length    *int64  `json:"length"`
	Value     any     `json:"value"`
	Next      *string `json:"next"`
	Truncated bool    `json:"truncated"`
	Encoding  *string `json:"encoding"`
}

// RedisDB is one RedisInfoResponse.dbs item.
type RedisDB struct {
	DB      int   `json:"db"`
	Keys    int64 `json:"keys"`
	Expires int64 `json:"expires"`
}

// SlowlogEntry is one RedisInfoResponse.slowlog item.
type SlowlogEntry struct {
	ID         int64  `json:"id"`
	At         string `json:"at"`
	DurationUs int64  `json:"durationUs"`
	Command    string `json:"command"`
}

// RedisInfoResponse is TS RedisInfoResponse.
type RedisInfoResponse struct {
	ServerVersion *string                      `json:"serverVersion"`
	Sections      map[string]map[string]string `json:"sections"`
	DBs           []RedisDB                    `json:"dbs"`
	Slowlog       []SlowlogEntry               `json:"slowlog"`
}

// MqttPublishRequest is TS MqttPublishRequest.
type MqttPublishRequest struct {
	Topic    string `json:"topic"`
	Payload  string `json:"payload"`
	Encoding string `json:"encoding"`
	QoS      int    `json:"qos"`
	Retain   bool   `json:"retain"`
	Confirm  string `json:"confirm"`
}

// MqttPublishResponse is TS MqttPublishResponse.
type MqttPublishResponse struct {
	OK         bool    `json:"ok"`
	DurationMs int64   `json:"durationMs"`
	Bytes      int     `json:"bytes"`
	ActivityID *string `json:"activityId"`
}

// MqttSubscription is TS MqttSubscription.
type MqttSubscription struct {
	Filter string `json:"filter"`
	QoS    int    `json:"qos"`
}

// MqttSessionRequest is TS MqttSessionRequest.
type MqttSessionRequest struct {
	Subscriptions []MqttSubscription `json:"subscriptions"`
}

// MqttSessionResponse is TS MqttSessionResponse.
type MqttSessionResponse struct {
	SessionID  string `json:"sessionId"`
	Ticket     string `json:"ticket"`
	StreamPath string `json:"streamPath"`
	ExpiresAt  string `json:"expiresAt"`
}

// MqttMessage is TS MqttMessage.
type MqttMessage struct {
	Seq       int64   `json:"seq"`
	At        string  `json:"at"`
	Topic     string  `json:"topic"`
	QoS       int     `json:"qos"`
	Retained  bool    `json:"retained"`
	Duplicate bool    `json:"duplicate"`
	SizeBytes int     `json:"sizeBytes"`
	Text      *string `json:"text"`
	Base64    *string `json:"base64"`
	Truncated bool    `json:"truncated"`
}

// MqttTopicSeen is TS MqttTopicSeen.
type MqttTopicSeen struct {
	Topic         string  `json:"topic"`
	Messages      int64   `json:"messages"`
	LastAt        string  `json:"lastAt"`
	Retained      bool    `json:"retained"`
	LastPreview   *string `json:"lastPreview"`
	LastSizeBytes int     `json:"lastSizeBytes"`
}

// HTTPKV is TS HttpKV.
type HTTPKV struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Enabled bool   `json:"enabled"`
}

// HTTPBodyField is one form / multipart field.
type HTTPBodyField struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Enabled  bool    `json:"enabled"`
	FileName *string `json:"fileName,omitempty"`
	Base64   bool    `json:"base64,omitempty"`
}

// HTTPBody is TS HttpBody (tagged by kind).
type HTTPBody struct {
	Kind        string          `json:"kind"` // none | json | text | form | multipart | binary
	Text        string          `json:"text,omitempty"`
	ContentType *string         `json:"contentType,omitempty"`
	Fields      []HTTPBodyField `json:"fields,omitempty"`
	Base64      string          `json:"base64,omitempty"`
}

// HTTPAuth is HttpRequestSpec.auth.
type HTTPAuth struct {
	Scheme       string  `json:"scheme"`
	Username     *string `json:"username"`
	Password     *string `json:"password"`
	Token        *string `json:"token"`
	APIKeyHeader *string `json:"apiKeyHeader"`
	APIKeyIn     string  `json:"apiKeyIn"`
}

// HTTPRequestSpec is TS HttpRequestSpec.
type HTTPRequestSpec struct {
	Method          string    `json:"method"`
	URL             string    `json:"url"`
	Query           []HTTPKV  `json:"query"`
	Headers         []HTTPKV  `json:"headers"`
	Body            HTTPBody  `json:"body"`
	Auth            *HTTPAuth `json:"auth"`
	FollowRedirects *bool     `json:"followRedirects"`
	TLSInsecure     *bool     `json:"tlsInsecure"`
	TimeoutSec      *int      `json:"timeoutSec"`
}

// ExecuteHTTPRequest is TS ExecuteHttpRequest.
type ExecuteHTTPRequest struct {
	Request       HTTPRequestSpec   `json:"request"`
	EnvironmentID *string           `json:"environmentId"`
	Variables     map[string]string `json:"variables"`
	QueryID       string            `json:"queryId"`
	Confirm       string            `json:"confirm"`
}

// HTTPTiming is TS HttpTiming.
type HTTPTiming struct {
	DNSMs      *int64 `json:"dnsMs"`
	ConnectMs  *int64 `json:"connectMs"`
	TLSMs      *int64 `json:"tlsMs"`
	TTFBMs     *int64 `json:"ttfbMs"`
	DownloadMs *int64 `json:"downloadMs"`
	TotalMs    int64  `json:"totalMs"`
}

// HTTPRedirect is one redirect of the chain.
type HTTPRedirect struct {
	Status int    `json:"status"`
	URL    string `json:"url"`
}

// HTTPCookie is one ExecuteHttpResponse.cookies item.
type HTTPCookie struct {
	Name     string  `json:"name"`
	Value    string  `json:"value"`
	Domain   *string `json:"domain"`
	Path     *string `json:"path"`
	Expires  *string `json:"expires"`
	HTTPOnly bool    `json:"httpOnly"`
	Secure   bool    `json:"secure"`
}

// HTTPSent is ExecuteHttpResponse.sent.
type HTTPSent struct {
	Method    string      `json:"method"`
	URL       string      `json:"url"`
	Headers   [][2]string `json:"headers"`
	BodyBytes int64       `json:"bodyBytes"`
}

// ExecuteHTTPResponse is TS ExecuteHttpResponse.
type ExecuteHTTPResponse struct {
	QueryID       string         `json:"queryId"`
	OK            bool           `json:"ok"`
	Error         *string        `json:"error"`
	Status        *int           `json:"status"`
	StatusText    *string        `json:"statusText"`
	Protocol      *string        `json:"protocol"`
	FinalURL      *string        `json:"finalUrl"`
	Redirects     []HTTPRedirect `json:"redirects"`
	Headers       [][2]string    `json:"headers"`
	Cookies       []HTTPCookie   `json:"cookies"`
	ContentType   *string        `json:"contentType"`
	SizeBytes     int64          `json:"sizeBytes"`
	HeaderBytes   int64          `json:"headerBytes"`
	BodyText      *string        `json:"bodyText"`
	BodyBase64    *string        `json:"bodyBase64"`
	BodyTruncated bool           `json:"bodyTruncated"`
	ResultID      *string        `json:"resultId"`
	Timing        HTTPTiming     `json:"timing"`
	Sent          HTTPSent       `json:"sent"`
	ActivityID    *string        `json:"activityId"`
}

// CancelRequest is TS CancelRequest.
type CancelRequest struct {
	QueryID string `json:"queryId"`
}

// ───────────────────────── saved, environments, history

// Saved is TS DevToolSaved.
type Saved struct {
	ID           string  `json:"id"`
	Kind         string  `json:"kind"`
	ConnectionID *string `json:"connectionId"`
	Collection   string  `json:"collection"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Content      string  `json:"content"`
	CreatedBy    *string `json:"createdBy"`
	CreatedAt    string  `json:"createdAt"`
	UpdatedAt    string  `json:"updatedAt"`
}

// SavedInput is TS DevToolSavedInput.
type SavedInput struct {
	Kind         string  `json:"kind"`
	ConnectionID *string `json:"connectionId"`
	Collection   string  `json:"collection"`
	Name         string  `json:"name"`
	Description  string  `json:"description"`
	Content      string  `json:"content"`
}

// EnvVar is one environment variable.
type EnvVar struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Secret  bool   `json:"secret"`
	Enabled bool   `json:"enabled"`
}

// Environment is TS DevToolEnvironment.
type Environment struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Variables []EnvVar `json:"variables"`
	CreatedBy *string  `json:"createdBy"`
	UpdatedAt string   `json:"updatedAt"`
}

// EnvironmentInput is TS DevToolEnvironmentInput.
type EnvironmentInput struct {
	Name      string   `json:"name"`
	Variables []EnvVar `json:"variables"`
}

// HistoryItem is TS DevToolHistoryItem.
type HistoryItem struct {
	ActivityID     string  `json:"activityId"`
	Ts             string  `json:"ts"`
	Actor          *string `json:"actor"`
	ConnectionID   string  `json:"connectionId"`
	ConnectionName string  `json:"connectionName"`
	Kind           string  `json:"kind"`
	Op             string  `json:"op"`
	Statement      string  `json:"statement"`
	OK             bool    `json:"ok"`
	DurationMs     int64   `json:"durationMs"`
	Rows           *int64  `json:"rows"`
	Status         *int    `json:"status"`
	Error          *string `json:"error"`
	Via            *string `json:"via"`
}

// RevealResponse is TS RevealResponse.
type RevealResponse struct {
	Value        string `json:"value"`
	HideAfterSec int    `json:"hideAfterSec"`
}
