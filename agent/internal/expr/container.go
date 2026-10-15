package expr

import (
	"errors"
	"strings"
)

// EnvSource is one env var of a container: the container's own item (field env) or the image default (field imageEnv).
// Src nil = no value (image env without default, or a secretRef). SecretRef: the plain secret value of a secretRef item
// (Src must be nil); SecretRef.Value nil = the secret has no value.
type EnvSource struct {
	Name      string        `json:"name"`
	Src       *string       `json:"src"`
	Field     Field         `json:"field"`
	SecretRef *SecretRefVal `json:"secretRef,omitempty"`
}

// SecretRefVal is the plain value of a secretRef env item.
type SecretRefVal struct {
	Value *string `json:"value"`
}

// MountSource is one mount of a container (explicit volume or implicit image volume).
// Key: e.g. "volume[0]" or "image:<name>". FileSlug: file volumes (file.<slug>.path answers this mount's resolved path).
type MountSource struct {
	Key      string  `json:"key"`
	Src      string  `json:"src"`
	FileSlug *string `json:"fileSlug,omitempty"`
}

// Resolved is one resolution result: Result or Err (TS Resolved<ResolveResult>).
type Resolved struct {
	Result *Result
	Err    *Error
}

// ContainerResolution is the result of ResolveContainer (TS ContainerResolution).
type ContainerResolution struct {
	Env    map[string]Resolved
	Mounts map[string]Resolved
}

// ResolveContainer resolves every env var and mount path of ONE container (SPEC §10.5, TS resolveContainer):
// lazily, in dependency order — container.env.X resolves X first, file.<slug>.path resolves that file's mount first.
// A cycle gives E_CYCLE for every value on it; a value that uses a failed value fails with the same code at its own
// reference. base.File is ignored for file slugs this container mounts (their resolved mount path is used).
func ResolveContainer(base *Context, env []EnvSource, mounts []MountSource) *ContainerResolution {
	envBy := map[string]*EnvSource{}
	for i := range env { // last wins, as TS new Map(...)
		envBy[env[i].Name] = &env[i]
	}
	mountBy := map[string]*MountSource{}
	for i := range mounts {
		mountBy[mounts[i].Key] = &mounts[i]
	}
	fileMount := map[string]string{}
	for _, m := range mounts {
		if m.FileSlug != nil && *m.FileSlug != "" {
			if _, ok := fileMount[*m.FileSlug]; !ok {
				fileMount[*m.FileSlug] = m.Key
			}
		}
	}
	out := &ContainerResolution{Env: map[string]Resolved{}, Mounts: map[string]Resolved{}}
	var stack []string // node ids "env:NAME" / "mount:KEY"
	label := func(id string) string {
		if strings.HasPrefix(id, "env:") {
			return "container.env." + id[4:]
		}
		return "mount " + id[6:]
	}
	ctx := *base
	if base.Container != nil {
		c := *base.Container
		c.Env = nil
		ctx.Container = &c
	}

	var node func(id string) Resolved
	dep := func(id string, p *Path) (*Result, *Error) {
		for i, s := range stack {
			if s == id {
				chain := make([]string, 0, len(stack)-i+1)
				for _, x := range stack[i:] {
					chain = append(chain, label(x))
				}
				chain = append(chain, label(id))
				return nil, errf(ECycle, p.Pos, "cycle: %s", strings.Join(chain, " → "))
			}
		}
		r := node(id)
		if r.Err != nil {
			return nil, &Error{Code: r.Err.Code, Message: label(id) + ": " + r.Err.Message, Pos: p.Pos}
		}
		return r.Result, nil
	}
	hooks := &Hooks{
		Env: func(name string, p *Path) (*EnvRef, *Error) {
			e, ok := envBy[name]
			if !ok {
				return nil, nil
			}
			r, err := dep("env:"+name, p)
			if err != nil {
				return nil, err
			}
			var value *string
			switch {
			case e.SecretRef != nil:
				value = e.SecretRef.Value
			case e.Src == nil:
				value = nil
			default:
				v := r.Value
				value = &v
			}
			return &EnvRef{Value: value, Secret: r.Secret}, nil
		},
		File: func(slug string, p *Path) (*FileRef, *Error) {
			key, ok := fileMount[slug]
			if !ok {
				if f, ok := base.File[slug]; ok {
					return &f, nil
				}
				return nil, nil
			}
			r, err := dep("mount:"+key, p)
			if err != nil {
				return nil, err
			}
			v := r.Value
			return &FileRef{Path: &v}, nil
		},
	}
	node = func(id string) Resolved {
		isEnv := strings.HasPrefix(id, "env:")
		var key string
		if isEnv {
			key = id[4:]
			if m, ok := out.Env[key]; ok {
				return m
			}
		} else {
			key = id[6:]
			if m, ok := out.Mounts[key]; ok {
				return m
			}
		}
		stack = append(stack, id)
		var res Resolved
		var err error
		var r *Result
		if isEnv {
			e := envBy[key]
			switch {
			case e.SecretRef != nil:
				v := ""
				if e.SecretRef.Value != nil {
					v = *e.SecretRef.Value
				}
				r = &Result{Value: v, Masked: "••••", Secret: true, Refs: []string{}}
			case e.Src == nil:
				r = &Result{Value: "", Masked: "", Secret: false, Refs: []string{}}
			default:
				r, err = ResolveWithHooks(*e.Src, &ctx, e.Field, hooks)
			}
		} else {
			r, err = ResolveWithHooks(mountBy[key].Src, &ctx, FieldMountPath, hooks)
		}
		if err != nil {
			var xe *Error
			if !errors.As(err, &xe) {
				xe = &Error{Code: ESyntax, Message: err.Error()}
			}
			res = Resolved{Err: xe}
		} else {
			res = Resolved{Result: r}
		}
		stack = stack[:len(stack)-1]
		if isEnv {
			out.Env[key] = res
		} else {
			out.Mounts[key] = res
		}
		return res
	}
	for _, e := range env {
		node("env:" + e.Name)
	}
	for _, m := range mounts {
		node("mount:" + m.Key)
	}
	return out
}
