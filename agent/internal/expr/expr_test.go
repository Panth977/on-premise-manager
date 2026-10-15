package expr

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
)

type vecs struct {
	Contexts map[string]*Context `json:"contexts"`
	Cases    []struct {
		Name   string   `json:"name"`
		Field  Field    `json:"field"`
		Ctx    string   `json:"ctx"`
		Input  string   `json:"input"`
		Output *string  `json:"output"`
		Number *int     `json:"number"`
		Masked string   `json:"masked"`
		Secret bool     `json:"secret"`
		Refs   []string `json:"refs"`
		Error  *struct {
			Code string `json:"code"`
			Pos  int    `json:"pos"`
		} `json:"error"`
	} `json:"cases"`
	Check []struct {
		Name  string `json:"name"`
		Field Field  `json:"field"`
		Input string `json:"input"`
		Error *struct {
			Code string `json:"code"`
			Pos  int    `json:"pos"`
		} `json:"error"`
	} `json:"check"`
}

func load(t *testing.T) vecs {
	b, err := os.ReadFile("../../../shared/testdata/expr-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v vecs
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func TestVectors(t *testing.T) {
	v := load(t)
	if len(v.Cases) < 100 {
		t.Fatalf("only %d cases", len(v.Cases))
	}
	for _, c := range v.Cases {
		t.Run(c.Name, func(t *testing.T) {
			r, err := Resolve(c.Input, v.Contexts[c.Ctx], c.Field)
			if c.Error != nil {
				var e *Error
				if !errors.As(err, &e) {
					t.Fatalf("want %s@%d, got result %+v err %v", c.Error.Code, c.Error.Pos, r, err)
				}
				if e.Code != c.Error.Code || e.Pos != c.Error.Pos {
					t.Fatalf("want %s@%d, got %s@%d (%s)", c.Error.Code, c.Error.Pos, e.Code, e.Pos, e.Message)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error %v", err)
			}
			if r.Value != *c.Output || r.Masked != c.Masked || r.Secret != c.Secret || !reflect.DeepEqual(r.Refs, c.Refs) {
				t.Fatalf("got %+v want output=%q masked=%q secret=%v refs=%v", r, *c.Output, c.Masked, c.Secret, c.Refs)
			}
			if (c.Number == nil) != (r.Number == nil) || (c.Number != nil && *c.Number != *r.Number) {
				t.Fatalf("number got %v want %v", r.Number, c.Number)
			}
		})
	}
}

func TestCheckVectors(t *testing.T) {
	v := load(t)
	for _, c := range v.Check {
		errs := CheckExpr(c.Input, c.Field)
		if c.Error == nil {
			if len(errs) != 0 {
				t.Errorf("%s: unexpected %v", c.Name, errs[0])
			}
			continue
		}
		if len(errs) != 1 || errs[0].Code != c.Error.Code || errs[0].Pos != c.Error.Pos {
			t.Errorf("%s: want %s@%d got %v", c.Name, c.Error.Code, c.Error.Pos, errs)
		}
	}
}

func TestNumberField(t *testing.T) {
	ctx := load(t).Contexts["app-api"]
	if n, err := ResolveNumberField(5432.0, ctx, FieldHostPort); err != nil || *n != 5432 {
		t.Fatal(n, err)
	}
	if n, err := ResolveNumberField(nil, ctx, FieldHostPort); err != nil || n != nil {
		t.Fatal(n, err)
	}
	if n, err := ResolveNumberField("{{ var.api-port }}", ctx, FieldHostPort); err != nil || *n != 8080 {
		t.Fatal(n, err)
	}
	if _, err := ResolveNumberField("8080", ctx, FieldImagePort); err == nil {
		t.Fatal("want E_TYPE")
	}
	refs, _ := ExprRefs(`a{{ var.x }}b{{ "lit" }}{{ site.db.host | upper }}`)
	if !reflect.DeepEqual(refs, []string{"var.x", "site.db.host"}) {
		t.Fatal(refs)
	}
}

type wantRes struct {
	Value  *string `json:"value"`
	Masked string  `json:"masked"`
	Secret bool    `json:"secret"`
	Error  *struct {
		Code string `json:"code"`
		Pos  int    `json:"pos"`
	} `json:"error"`
}

func TestContainerVectors(t *testing.T) {
	b, err := os.ReadFile("../../../shared/testdata/expr-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Contexts   map[string]*Context `json:"contexts"`
		Containers []struct {
			Name   string        `json:"name"`
			Ctx    string        `json:"ctx"`
			Env    []EnvSource   `json:"env"`
			Mounts []MountSource `json:"mounts"`
			Expect struct {
				Env    map[string]wantRes `json:"env"`
				Mounts map[string]wantRes `json:"mounts"`
			} `json:"expect"`
		} `json:"containers"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Containers) < 8 {
		t.Fatalf("only %d container cases", len(v.Containers))
	}
	cmp := func(t *testing.T, what string, got map[string]Resolved, want map[string]wantRes) {
		if len(got) != len(want) {
			t.Errorf("%s: got %d results, want %d", what, len(got), len(want))
		}
		for k, w := range want {
			g, ok := got[k]
			if !ok {
				t.Errorf("%s %s: missing", what, k)
				continue
			}
			if w.Error != nil {
				if g.Err == nil || g.Err.Code != w.Error.Code || g.Err.Pos != w.Error.Pos {
					t.Errorf("%s %s: want %s@%d, got %+v / %+v", what, k, w.Error.Code, w.Error.Pos, g.Result, g.Err)
				}
				continue
			}
			if g.Err != nil {
				t.Errorf("%s %s: unexpected %v", what, k, g.Err)
				continue
			}
			if g.Result.Value != *w.Value || g.Result.Masked != w.Masked || g.Result.Secret != w.Secret {
				t.Errorf("%s %s: got %+v want %q %q %v", what, k, g.Result, *w.Value, w.Masked, w.Secret)
			}
		}
	}
	for _, c := range v.Containers {
		t.Run(c.Name, func(t *testing.T) {
			r := ResolveContainer(v.Contexts[c.Ctx], c.Env, c.Mounts)
			cmp(t, "env", r.Env, c.Expect.Env)
			cmp(t, "mount", r.Mounts, c.Expect.Mounts)
		})
	}
}
