package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConfigBlobs(t *testing.T) {
	sha := func(c string) string { return "sha256:" + strings.Repeat(c, 64) }
	raw := `{"files":[{"fileSlug":"f","content":{"sha256":"` + sha("a") + `","sizeBytes":3}}],"sites":[{"siteSlug":"s","label":"","config":{"volumes":[
	{"volumeSlug":"t","label":"","description":"","backup":false,"seed":{"kind":"tree","entries":[{"path":"x","sha256":"` + sha("b") + `","size":5,"mode":"0644","render":"none"},{"path":"y","sha256":"` + sha("a") + `","size":3,"mode":"0644","render":"none"}],"dirs":[]}},
	{"volumeSlug":"o","label":"","description":"","backup":false,"kind":"file","seed":{"kind":"single","sha256":"` + sha("c") + `","size":7,"mode":"0644","render":"none"}},
	{"volumeSlug":"r","label":"","description":"","backup":false,"seed":{"kind":"archive","blobSha256":"` + sha("d") + `","name":"x.tar.gz","size":11}},
	{"volumeSlug":"n","label":"","description":"","backup":false,"seed":null}]}}]}`
	var c Config
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		t.Fatal(err)
	}
	l := ConfigBlobs(c.Files, c.Sites)
	if len(l) != 4 || l[0].Sha256 != sha("a") || l[1].SizeBytes != 5 || l[2].SizeBytes != 7 || l[3].Sha256 != sha("d") || l[3].SizeBytes != 11 {
		t.Fatalf("%+v", l)
	}
	next := c
	next.Files = []FileDef{{FileSlug: "f", Content: &BlobRef{Sha256: sha("e"), SizeBytes: 9}}}
	n := NewConfigBlobs(&c, &next)
	if len(n) != 1 || n[0].Sha256 != sha("e") {
		t.Fatalf("new %+v", n)
	}
	if len(NewConfigBlobs(nil, &c)) != 4 {
		t.Fatal("nil base")
	}
}
