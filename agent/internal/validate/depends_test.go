package validate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"opm/agent/internal/model"
)

// §21 #93: shared/testdata/depends-vectors.json — the same vectors as shared/src/depends.test.ts.
type dependsVectors struct {
	Literal []struct {
		Text   string   `json:"text"`
		Slugs  []string `json:"slugs"`
		Expect []string `json:"expect"`
	} `json:"literal"`
	Infer []struct {
		Name         string                                    `json:"name"`
		Site         model.SiteConfig                          `json:"site"`
		Images       map[string]struct{ Env []model.ImageEnv } `json:"images"`
		Files        []model.FileDef                           `json:"files"`
		FileContents map[string]string                         `json:"fileContents"`
		Expect       map[string]struct {
			Explicit  []string         `json:"explicit"`
			Implicit  []InferredDepend `json:"implicit"`
			Suggested []InferredDepend `json:"suggested"`
		} `json:"expect"`
		Order []string `json:"order"`
		Cycle []string `json:"cycle"`
	} `json:"infer"`
	DNS []struct {
		Name    string          `json:"name"`
		Subject string          `json:"subject"`
		Slugs   []string        `json:"slugs"`
		Lines   []string        `json:"lines"`
		Expect  *DNSFailureHint `json:"expect"`
		Message *string         `json:"message"`
	} `json:"dns"`
}

func loadDependsVectors(t *testing.T) dependsVectors {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "..", "shared", "testdata", "depends-vectors.json"))
	if err != nil {
		t.Fatal(err)
	}
	var v dependsVectors
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDependsVectors(t *testing.T) {
	v := loadDependsVectors(t)
	if len(v.Literal) < 10 || len(v.Infer) < 5 || len(v.DNS) < 8 {
		t.Fatalf("vectors not loaded: %d %d %d", len(v.Literal), len(v.Infer), len(v.DNS))
	}
	for _, c := range v.Literal {
		got := LiteralHostMentions(c.Text, c.Slugs)
		if len(got) == 0 && len(c.Expect) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, c.Expect) {
			t.Errorf("literal %q: got %v want %v", c.Text, got, c.Expect)
		}
	}
	for _, c := range v.Infer {
		imgs := map[string]CatalogImage{}
		for k, im := range c.Images {
			imgs[k] = CatalogImage{Env: im.Env}
		}
		site := c.Site
		inf := InferDepends(DependsInput{Site: &site, Images: imgs, Files: c.Files, FileContents: c.FileContents})
		if len(inf) != len(c.Expect) {
			t.Errorf("%s: %d containers, want %d", c.Name, len(inf), len(c.Expect))
		}
		for _, d := range inf {
			w := c.Expect[d.ContainerSlug]
			if !reflect.DeepEqual(d.Explicit, w.Explicit) || !reflect.DeepEqual(d.Implicit, w.Implicit) || !reflect.DeepEqual(d.Suggested, w.Suggested) {
				t.Errorf("%s / %s: got %+v want %+v", c.Name, d.ContainerSlug, d, w)
			}
		}
		order, cycle := StartOrder(&site, inf)
		if !reflect.DeepEqual(order, c.Order) || !reflect.DeepEqual(cycle, c.Cycle) {
			t.Errorf("%s: order %v cycle %v, want %v %v", c.Name, order, cycle, c.Order, c.Cycle)
		}
	}
	for _, c := range v.DNS {
		h := DNSFailureHintOf(c.Lines, c.Slugs, c.Subject)
		if !reflect.DeepEqual(h, c.Expect) {
			t.Errorf("dns %s: got %+v want %+v", c.Name, h, c.Expect)
			continue
		}
		if h != nil && DNSFailureMessage(c.Subject, h) != *c.Message {
			t.Errorf("dns %s: message %q", c.Name, DNSFailureMessage(c.Subject, h))
		}
	}
}
