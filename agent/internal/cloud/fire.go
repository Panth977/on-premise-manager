package cloud

import (
	"strings"

	"opm/agent/internal/cloud/doc"
	"opm/agent/internal/cloud/driver"
)

var ports *driver.Ports

// SetPorts installs the driver set (Serve, the operator commands, tests).
func SetPorts(p *driver.Ports) { ports = p }

// DB is the document store.
func DB() *driver.Store { return ports.Store }

// Blobs is the object store.
func Blobs() *driver.BlobStore { return ports.Blobs }

// AuthAdmin is the user store.
func AuthAdmin() *driver.Auth { return ports.Auth }

// Pub removes internal fields (prefixed "_") before a document leaves the API.
func Pub(d *doc.Map) *doc.Map {
	out := doc.New()
	d.Each(func(k string, v any) {
		if !strings.HasPrefix(k, "_") {
			out.Set(k, v)
		}
	})
	return out
}

func depSub(d, coll string) *driver.CollectionRef {
	return DB().Collection("deployments").Doc(d).Collection(coll)
}

// Col holds the collection helpers of fire.ts.
var Col colT

type colT struct{}

func (colT) Registries() *driver.CollectionRef { return DB().Collection("registries") }
func (colT) Services() *driver.CollectionRef   { return DB().Collection("services") }
func (colT) Images(s string) *driver.CollectionRef {
	return DB().Collection("services").Doc(s).Collection("images")
}
func (colT) NotFoundPages() *driver.CollectionRef         { return DB().Collection("notFoundPages") }
func (colT) Tags() *driver.CollectionRef                  { return DB().Collection("tags") }
func (colT) Icons() *driver.CollectionRef                 { return DB().Collection("icons") }
func (colT) Accounts() *driver.CollectionRef              { return DB().Collection("accounts") }
func (colT) AuditLog() *driver.CollectionRef              { return DB().Collection("auditLog") }
func (colT) Templates() *driver.CollectionRef             { return DB().Collection("templates") }
func (colT) Deployments() *driver.CollectionRef           { return DB().Collection("deployments") }
func (colT) Revisions(d string) *driver.CollectionRef     { return depSub(d, "revisions") }
func (colT) Proposals(d string) *driver.CollectionRef     { return depSub(d, "proposals") }
func (colT) Agents(d string) *driver.CollectionRef        { return depSub(d, "agents") }
func (colT) InstallTokens(d string) *driver.CollectionRef { return depSub(d, "installTokens") }
func (colT) Activities(d string) *driver.CollectionRef    { return depSub(d, "activities") }
func (colT) Transfers(d string) *driver.CollectionRef     { return depSub(d, "transfers") }
func (colT) Files(d string) *driver.CollectionRef         { return depSub(d, "files") }
func (colT) Uptime(d string) *driver.CollectionRef        { return depSub(d, "uptime") }
func (colT) RevokedKeys(d string) *driver.CollectionRef   { return depSub(d, "revokedKeys") }
func (colT) Uploads() *driver.CollectionRef               { return DB().Collection("uploads") }
func (colT) AgentNonces() *driver.CollectionRef           { return DB().Collection("agentNonces") }
