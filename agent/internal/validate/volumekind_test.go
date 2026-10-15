package validate

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"opm/agent/internal/model"
)

type vkIssue struct {
	Path     string `json:"path"`
	Code     string `json:"code"`
	Severity string `json:"severity"`
}

// shared/testdata/volume-kind-vectors.json (§21 #83): image volume kind + the container mount kind matrix.
// Mirrors shared/src/volume-kind.test.ts.
func TestVolumeKindVectors(t *testing.T) {
	b, err := os.ReadFile("../../../shared/testdata/volume-kind-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Codes []string `json:"codes"`
		Modes []Mode   `json:"modes"`
		Infer []struct {
			MountPath string `json:"mountPath"`
			Kind      string `json:"kind"`
		} `json:"infer"`
		ImageVolumes []struct {
			Name   string            `json:"name"`
			Volume model.ImageVolume `json:"volume"`
			Expect []vkIssue         `json:"expect"`
		} `json:"imageVolumes"`
		Mounts []struct {
			Name      string  `json:"name"`
			ImagePath string  `json:"imagePath"`
			ImageKind *string `json:"imageKind"`
			SiteKind  string  `json:"siteKind"`
			Row       struct {
				ImageVolume *string `json:"imageVolume"`
				MountPath   *string `json:"mountPath"`
				ReadOnly    bool    `json:"readOnly"`
				Source      string  `json:"source"`
			} `json:"row"`
			Expect []vkIssue `json:"expect"`
		} `json:"mounts"`
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	for _, x := range v.Infer {
		if got := InferImageVolumeKind(x.MountPath); got != x.Kind {
			t.Errorf("infer %s: got %s want %s", x.MountPath, got, x.Kind)
		}
	}
	for _, c := range v.ImageVolumes {
		is := NewIssues(ModePublish)
		ValidateImageVolumes([]model.ImageVolume{c.Volume}, is, "volumes")
		got := []vkIssue{}
		for _, x := range is.List {
			got = append(got, vkIssue{x.Path, x.Code, x.Severity})
		}
		want := append([]vkIssue{}, c.Expect...)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("image %s:\n got %v\nwant %v", c.Name, got, want)
		}
	}
	for _, c := range v.Mounts {
		for _, mode := range v.Modes {
			cfg, ctx := load(t, mode)
			cfg = clone(t, cfg)
			im := ctx.Images["hello-api:v1"]
			im.Volumes = append(append([]model.ImageVolume{}, im.Volumes...), model.ImageVolume{Name: "t", MountPath: c.ImagePath, Kind: c.ImageKind})
			ctx.Images["hello-api:v1"] = im
			app := &site(cfg, "app").Config
			vs := append(*app.Volumes, newVol("tv", c.SiteKind, nil))
			app.Volumes = &vs
			var src model.VolumeSource
			switch c.Row.Source {
			case "volume":
				src = model.VolumeSource{Kind: "volume", VolumeSlug: "tv"}
			case "file":
				src = model.VolumeSource{Kind: "file", FileSlug: "banner"}
			default:
				src = model.VolumeSource{Kind: "secret", SecretSlug: "api-token"}
			}
			for i := range app.Containers {
				if app.Containers[i].ContainerSlug == "hello-api" {
					app.Containers[i].Volumes = append(app.Containers[i].Volumes, model.ContainerMount{ImageVolume: c.Row.ImageVolume, MountPath: c.Row.MountPath, ReadOnly: c.Row.ReadOnly, Source: src})
				}
			}
			got := []vkIssue{}
			for _, x := range ValidateConfig(cfg, ctx).List {
				if contains(v.Codes, x.Code) {
					got = append(got, vkIssue{x.Path, x.Code, x.Severity})
				}
			}
			want := append([]vkIssue{}, c.Expect...)
			if !reflect.DeepEqual(got, want) {
				t.Errorf("%s (%s):\n got %v\nwant %v", c.Name, mode, got, want)
			}
		}
	}
}
