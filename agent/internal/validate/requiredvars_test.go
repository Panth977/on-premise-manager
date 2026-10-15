package validate

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"opm/agent/internal/model"
)

// shared/testdata/required-vars-vectors.json (round E): required variables / secrets are set at deployment.
func TestRequiredVarsVectors(t *testing.T) {
	b, err := os.ReadFile("../../../shared/testdata/required-vars-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Codes []string `json:"codes"`
		Cases []struct {
			Name     string `json:"name"`
			Mode     Mode   `json:"mode"`
			Variable struct {
				Required bool `json:"required"`
				Value    any  `json:"value"`
			} `json:"variable"`
			SecretValue *string `json:"secretValue"`
			Expect      []struct {
				Path     string `json:"path"`
				Code     string `json:"code"`
				Severity string `json:"severity"`
			} `json:"expect"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for _, c := range v.Cases {
		cfg, ctx := load(t, c.Mode)
		cfg = clone(t, cfg)
		cfg.Variables = append(cfg.Variables, model.Variable{VariableSlug: "server-ip", Label: "Server IP", Type: "string", Value: model.VariableValue{V: c.Variable.Value}, Required: c.Variable.Required})
		cfg.Secrets = append(cfg.Secrets, model.SecretDef{SecretSlug: "jwt-key", Label: "JWT key", Required: true, Value: c.SecretValue})
		app := &site(cfg, "app").Config
		h := "{{ var.server-ip }}"
		app.Host = &h
		for i := range app.Containers {
			u := "http://{{ var.server-ip | upper }}:8080"
			app.Containers[i].Env = append(app.Containers[i].Env, model.ContainerEnv{Name: "SERVER_URL", Value: &u})
		}
		j := "Bearer {{ secret.jwt-key }}"
		app.Containers[0].Env = append(app.Containers[0].Env, model.ContainerEnv{Name: "JWT", Value: &j})
		is := ValidateConfig(cfg, ctx)
		got := [][3]string{}
		for _, x := range is.List {
			if contains(v.Codes, x.Code) {
				got = append(got, [3]string{x.Path, x.Code, x.Severity})
			}
		}
		want := [][3]string{}
		for _, x := range c.Expect {
			want = append(want, [3]string{x.Path, x.Code, x.Severity})
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got %v\nwant %v", c.Name, got, want)
		}
	}
}
