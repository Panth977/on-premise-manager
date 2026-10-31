package driver

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// TriggerSpecs is TRIGGER_SPECS: every process that writes adds outbox rows for these patterns.
var TriggerSpecs = []OutboxTrigger{
	{"importImage", "written", "services/{serviceSlug}/images/{version}"},
	{"mirrorTemplateCommit", "created", "templates/{templateId}/commits/{commitId}"},
	{"mirrorDeploymentCommit", "created", "deployments/{deploymentId}/commits/{commitId}"},
	{"mirrorAgentCommit", "created", "deployments/{deploymentId}/agentBranches/{siteSlug}/commits/{commitId}"},
	{"mirrorVolumeVersion", "created", "deployments/{deploymentId}/volumeContent/{key}/versions/{versionId}"},
	{"mirrorQueue", "created", "githubMirror/config/queue/{itemId}"},
}

// Config of Open (localPorts).
type Config struct {
	DataDir   string
	PublicURL string
	// ConsoleURL: reset links / OIDC redirects (default PublicURL)
	ConsoleURL string
	// Triggers: nil = TriggerSpecs
	Triggers []OutboxTrigger
	// Strict: nil = env OPM_LOCAL_STRICT=1
	Strict *bool
	// Indexes: extra index-registry entries (tests, new collections)
	Indexes IndexRegistry
}

// Ports is the driver set: document store, blob store, auth.
type Ports struct {
	Store *Store
	Blobs *BlobStore
	Auth  *Auth
}

// Close closes the document store.
func (p *Ports) Close() error { return p.Store.Close() }

// Open is localPorts.
func Open(c Config) (*Ports, error) {
	if err := os.MkdirAll(c.DataDir, 0o777); err != nil {
		return nil, err
	}
	pub := strings.TrimRight(c.PublicURL, "/")
	strict := os.Getenv("OPM_LOCAL_STRICT") == "1"
	if c.Strict != nil {
		strict = *c.Strict
	}
	trig := c.Triggers
	if trig == nil {
		trig = TriggerSpecs
	}
	s, err := newStore(c.DataDir, MergeRegistry(DefaultRegistry, c.Indexes), strict, trig)
	if err != nil {
		return nil, err
	}
	b, err := newBlobStore(c.DataDir, pub)
	if err != nil {
		return nil, err
	}
	con := c.ConsoleURL
	if con == "" {
		con = pub
	}
	return &Ports{s, b, &Auth{Store: s, dataDir: c.DataDir, ConsoleURL: strings.TrimRight(con, "/")}}, nil
}

// BackupSqlite: consistent online copy of every .sqlite file under <dataDir>/db (VACUUM INTO, safe while opm-cloud
// runs) into outDir with the same relative layout. Returns the written files; existing targets are an error.
func BackupSqlite(dataDir, outDir string) ([]string, error) {
	src := filepath.Join(dataDir, "db")
	var files []string
	_ = filepath.WalkDir(src, func(p string, e os.DirEntry, err error) error {
		if err == nil && e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".sqlite") {
			files = append(files, p)
		}
		return nil
	})
	sort.Strings(files)
	var out []string
	for _, f := range files {
		rel, err := filepath.Rel(src, f)
		if err != nil {
			return out, err
		}
		dst := filepath.Join(outDir, rel)
		if _, err := os.Stat(dst); err == nil {
			return out, fmt.Errorf("backup target exists: %s", dst)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o777); err != nil {
			return out, err
		}
		db, err := sql.Open("sqlite", dsn(f))
		if err != nil {
			return out, err
		}
		_, err = db.ExecContext(bg, "VACUUM INTO ?", dst)
		_ = db.Close()
		if err != nil {
			return out, err
		}
		out = append(out, dst)
	}
	return out, nil
}
