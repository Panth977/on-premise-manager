package shared

import (
	"strings"

	"opm/agent/internal/cloud/doc"
)

// Value exports of shared/src/files.ts.
const (
	// FILE_MAX is FILE_MAX.
	FILE_MAX = 2 * 1024 * 1024 * 1024
	// FILE_NOTE_MAX is FILE_NOTE_MAX.
	FILE_NOTE_MAX = 500
	// FILE_TOMBSTONE_DAYS is FILE_TOMBSTONE_DAYS.
	FILE_TOMBSTONE_DAYS = 30
)

// SiteFileBadge is siteFileBadge.
func SiteFileBadge(f *doc.Map) string {
	switch {
	case f.Str("state") == "unpublished":
		return "unpublished"
	case f.Str("state") == "uploading":
		return "uploading"
	case f.Truthy("here"):
		return "here"
	}
	if job := f.Map("job"); job != nil {
		switch job.Str("phase") {
		case "failed":
			return "failed"
		case "waiting-cloud":
			return "waiting-cloud"
		}
		return "getting"
	}
	src := f.Map("sources")
	if len(src.Arr("peers")) > 0 || src.Truthy("cloud") || len(f.Arr("sites")) > 0 {
		return "available"
	}
	return "unavailable"
}

// CloudFileBadge is cloudFileBadge.
func CloudFileBadge(f *doc.Map) string {
	switch {
	case f.Str("state") == "unpublished":
		return "unpublished"
	case f.Str("state") == "uploading":
		return "uploading"
	}
	cloud := f.Map("cloud")
	if cloud.Truthy("stored") {
		return "here"
	}
	if fetch := cloud.Map("fetch"); fetch != nil {
		if fetch.Truthy("error") {
			return "failed"
		}
		return "getting"
	}
	if len(f.Arr("sites")) > 0 {
		return "available"
	}
	return "unavailable"
}

// FileCopiesText is fileCopiesText.
func FileCopiesText(f *doc.Map) string {
	var w []string
	if f.Map("cloud").Truthy("stored") {
		w = append(w, "cloud")
	}
	for _, s := range f.Arr("sites") {
		w = append(w, jStr(s))
	}
	if len(w) == 0 {
		return "no copy"
	}
	return strings.Join(w, ", ")
}

// FileOriginText is fileOriginText.
func FileOriginText(f *doc.Map) string {
	o := f.Map("origin")
	if o.Str("kind") == "cloud" {
		return "the cloud (browser)"
	}
	via := "browser"
	if o.Str("via") == "path" {
		via = jStr(jCoal(o.Val("sourcePath"), "server path"))
	}
	return jStr(o.Val("siteSlug")) + " (" + via + ")"
}
