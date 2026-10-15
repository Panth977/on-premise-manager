package model

import "sort"

// §21 #107 commit staging (shared/src/staging.ts): one Commit = one transaction incl. the bytes.

// StageTTLSeconds: stages older than this are removed (STAGE_TTL_MS = 24 h).
const StageTTLSeconds = 24 * 3600

// StageMaxBlobs / StageMaxBytes mirror STAGE_MAX_BLOBS / STAGE_MAX_BYTES.
const (
	StageMaxBlobs = 5000
	StageMaxBytes = 1024 * 1024 * 1024
)

// BlobNeed is TS BlobNeed.
type BlobNeed struct {
	Sha256    Sha256 `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

// CreateStageRequest is TS CreateStageRequest.
type CreateStageRequest struct {
	Blobs []BlobNeed `json:"blobs"`
}

// Stage is TS Stage.
type Stage struct {
	StageID   string     `json:"stageId"`
	ExpiresAt Ts         `json:"expiresAt"`
	Missing   []BlobNeed `json:"missing"`
	Present   int        `json:"present"`
}

// StageBlobResponse is TS StageBlobResponse.
type StageBlobResponse struct {
	Sha256    Sha256 `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
	Staged    bool   `json:"staged"`
}

// ConfigBlobs mirrors TS configBlobs: File contents + volume seeds (tree entries, single, archive), unique, sorted.
func ConfigBlobs(files []FileDef, sites []Site) []BlobNeed {
	seen := map[string]BlobNeed{}
	add := func(sha string, size int64) {
		if sha == "" {
			return
		}
		if len(sha) == 64 {
			sha = "sha256:" + sha
		}
		if len(sha) != 71 {
			return
		}
		if _, ok := seen[sha]; !ok {
			seen[sha] = BlobNeed{Sha256: sha, SizeBytes: size}
		}
	}
	for _, f := range files {
		if f.Content != nil {
			add(f.Content.Sha256, f.Content.SizeBytes)
		}
	}
	for _, s := range sites {
		if s.Config.Volumes == nil {
			continue
		}
		for _, v := range *s.Config.Volumes {
			if !v.Seed.Set || v.Seed.Value == nil {
				continue
			}
			sd := v.Seed.Value
			switch sd.Kind {
			case "tree":
				for _, e := range sd.Tree {
					add(e.Sha256, e.Size)
				}
			case "single":
				add(sd.Sha256, sd.Size)
			case "archive":
				add(sd.BlobSha256, sd.Size)
			}
		}
	}
	out := make([]BlobNeed, 0, len(seen))
	for _, b := range seen {
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sha256 < out[j].Sha256 })
	return out
}

// NewConfigBlobs mirrors TS newConfigBlobs: blobs of next that base does not reference.
func NewConfigBlobs(base *Config, next *Config) []BlobNeed {
	have := map[string]bool{}
	if base != nil {
		for _, b := range ConfigBlobs(base.Files, base.Sites) {
			have[b.Sha256] = true
		}
	}
	var out []BlobNeed
	for _, b := range ConfigBlobs(next.Files, next.Sites) {
		if !have[b.Sha256] {
			out = append(out, b)
		}
	}
	return out
}
