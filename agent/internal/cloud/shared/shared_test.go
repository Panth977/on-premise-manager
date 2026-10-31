package shared

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"opm/agent/internal/cloud/doc"
)

func tsBackend(t testing.TB) string {
	_, f, _, _ := runtime.Caller(0)
	d := filepath.Join(filepath.Dir(f), "../../../../backend")
	if _, err := os.Stat(filepath.Join(d, "node_modules/tsx")); err != nil {
		t.Skip("backend/node_modules missing")
	}
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("no node")
	}
	return d
}

// tsxRun runs a script through tsx from backend/ (router_test.go tsx).
func tsxRun(t testing.TB, script string, args ...string) []byte {
	t.Helper()
	_, f, _, _ := runtime.Caller(0)
	cmd := exec.Command("node", append([]string{"--import", "tsx", "--no-warnings=ExperimentalWarning", filepath.Join(filepath.Dir(f), script)}, args...)...)
	cmd.Dir = tsBackend(t)
	var out, e bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &e
	if err := cmd.Run(); err != nil {
		t.Fatalf("%s: %v\n%s", script, err, e.String())
	}
	return out.Bytes()
}

func sp(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func strsOf(v any) []string {
	out := []string{}
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return out
}

func mapOf(v any) *doc.Map {
	m, _ := v.(*doc.Map)
	return m
}

func issuesFn(f func(is *Issues, a []any) error) func(a []any) (any, error) {
	return func(a []any) (any, error) {
		is := NewIssues("publish")
		if err := f(is, a); err != nil {
			return nil, err
		}
		return is.List(), nil
	}
}

func catchFn(f func(a []any) any) func(a []any) (any, error) {
	return func(a []any) (out any, err error) {
		defer jCatch(&err)
		return f(a), nil
	}
}

func opt(a []any, i int) any {
	if i < len(a) {
		return a[i]
	}
	return doc.Undefined
}

// goFns is the Go side of every vector name of testdata/vectors.mts; a is the TS argument list.
var goFns = map[string]func(a []any) (any, error){
	"normalizeConfig":       func(a []any) (any, error) { return NormalizeConfig(a[0], mapOf(opt(a, 1))) },
	"normalizeSiteConfig":   func(a []any) (any, error) { return NormalizeSiteConfig(a[0], mapOf(opt(a, 1))) },
	"normalizeSiteVolume":   func(a []any) (any, error) { return NormalizeSiteVolume(a[0]) },
	"normalizeEdge":         func(a []any) (any, error) { return NormalizeEdge(a[0]) },
	"normalizeSiteDevTools": func(a []any) (any, error) { return NormalizeSiteDevTools(a[0]) },
	"normalizeContainer": func(a []any) (any, error) {
		o := opt(a, 2)
		return NormalizeContainer(a[0], jCoal(opt(a, 1), nil), mapOf(o).Bool("implicitVolumes"))
	},
	"normalizeLocation":    func(a []any) (any, error) { return NormalizeLocation(a[0]), nil },
	"normalizeImageEnv":    func(a []any) (any, error) { return NormalizeImageEnv(a[0]) },
	"normalizeImageVolume": func(a []any) (any, error) { return NormalizeImageVolume(a[0]) },
	"inferImageVolumeKind": func(a []any) (any, error) { return InferImageVolumeKind(a[0]), nil },
	"emptyConfig":          func(a []any) (any, error) { return EmptyConfig(), nil },
	"parseImageReference":  func(a []any) (any, error) { return ParseImageReference(a[0]), nil },
	"formatImageReference": func(a []any) (any, error) { return FormatImageReference(a[0].(*doc.Map)), nil },
	"registryHost":         func(a []any) (any, error) { return RegistryHost(a[0].(string)), nil },
	"matchRegistries":      func(a []any) (any, error) { return MatchRegistries(a[0].(string), a[1].([]any)) },
	"nextVersion": func(a []any) (any, error) {
		s, _ := a[0].(string)
		return NextVersion(s, strsOf(a[1])), nil
	},
	"checkIcon":         issuesFn(func(is *Issues, a []any) error { CheckIcon(a[0], "p", is); return nil }),
	"checkPicture":      issuesFn(func(is *Issues, a []any) error { CheckPicture(a[0], "p", is); return nil }),
	"iconUploadId":      func(a []any) (any, error) { return sp(IconUploadId(a[0])), nil },
	"checkRe2":          issuesFn(func(is *Issues, a []any) error { CheckRe2(a[0], "p", is); return nil }),
	"validateImageSpec": issuesFn(func(is *Issues, a []any) error { return ValidateImageSpec(a[0].(*doc.Map), is) }),
	"validateRegisterImage": func(a []any) (any, error) {
		is, err := ValidateRegisterImage(a[0].(*doc.Map))
		if err != nil {
			return nil, err
		}
		return is.List(), nil
	},
	"imageNameChanges": func(a []any) (any, error) { return ImageNameChanges(a[0].(*doc.Map), a[1].(*doc.Map), a[2]) },
	"imageNameUses": func(a []any) (any, error) {
		return ImageNameUses(a[0].(*doc.Map), a[1].(string), a[2].(string), a[3].(string), a[4].(string))
	},
	"renameImageNames": func(a []any) (any, error) {
		return RenameImageNames(a[0].(*doc.Map), a[1].(string), a[2].(string), a[3].([]any))
	},
	"renameImageSelfRefs":   func(a []any) (any, error) { return RenameImageSelfRefs(a[0].(*doc.Map), a[1].([]any)) },
	"imageNames":            func(a []any) (any, error) { return ImageNames(a[0].(*doc.Map)), nil },
	"compareAgentVersions":  func(a []any) (any, error) { return CompareAgentVersions(a[0].(string), a[1].(string)), nil },
	"agentUpgradeAvailable": func(a []any) (any, error) { return AgentUpgradeAvailable(a[0].(string), a[1].(string)), nil },
	"agentInstallAction":    func(a []any) (any, error) { return AgentInstallAction(a[0].(string), a[1].(string)), nil },
	"supportsGuiUpgrade":    func(a []any) (any, error) { return SupportsGuiUpgrade(a[0].(string)), nil },
	"agentSchemaCompatible": func(a []any) (any, error) { return AgentSchemaCompatible(a[0], a[1].(*doc.Map)), nil },
	"formatSkewDuration":    func(a []any) (any, error) { return FormatSkewDuration(a[0].(float64)), nil },
	"clockSkewWarning":      func(a []any) (any, error) { return sp(ClockSkewWarning(a[0])), nil },
	"installTokenState":     func(a []any) (any, error) { return InstallTokenState(a[0].(*doc.Map), int64(a[1].(float64))), nil },
	"isSiteImage":           func(a []any) (any, error) { return IsSiteImage(a[0].(*doc.Map)), nil },
	"auditActionMatches":    func(a []any) (any, error) { return AuditActionMatches(a[0].(string), a[1].(string)), nil },
	"activityHashInput":     func(a []any) (any, error) { return ActivityHashInput(a[0].(*doc.Map)) },
	"configBlobs":           func(a []any) (any, error) { return ConfigBlobs(a[0]) },
	"newConfigBlobs":        func(a []any) (any, error) { return NewConfigBlobs(a[0], a[1]) },
	"stagingObject":         func(a []any) (any, error) { return StagingObject(a[0].(string), a[1].(string)), nil },
	"blobHex":               func(a []any) (any, error) { return BlobHex(a[0].(string)), nil },
	"totalBytes":            func(a []any) (any, error) { return TotalBytes(a[0].([]any)), nil },
	"stageProgressText":     func(a []any) (any, error) { return sp(StageProgressText(mapOf(a[0]))), nil },
	"RE_STAGE_ID":           func(a []any) (any, error) { return RE_STAGE_ID.MatchString(a[0].(string)), nil },
	"siteFileBadge":         func(a []any) (any, error) { return SiteFileBadge(a[0].(*doc.Map)), nil },
	"cloudFileBadge":        func(a []any) (any, error) { return CloudFileBadge(a[0].(*doc.Map)), nil },
	"fileCopiesText":        func(a []any) (any, error) { return FileCopiesText(a[0].(*doc.Map)), nil },
	"fileOriginText":        func(a []any) (any, error) { return FileOriginText(a[0].(*doc.Map)), nil },
	"fsClean":               func(a []any) (any, error) { return sp(FsClean(a[0].(string))), nil },
	"fsParent":              func(a []any) (any, error) { return sp(FsParent(a[0].(string))), nil },
	"fsCrumbs":              func(a []any) (any, error) { return FsCrumbs(a[0].(string)), nil },
	"hostFsBlockReason":     func(a []any) (any, error) { return sp(HostFsBlockReason(a[0].(string), strsOf(a[1]))), nil },
	"hostFsVectors":         func(a []any) (any, error) { return a[0], nil },
	"transferState":         func(a []any) (any, error) { return TransferState(a[0].(*doc.Map), a[1].(string)), nil },
	"contentPathError":      func(a []any) (any, error) { return sp(ContentPathError(a[0])), nil },
	"excludePatternError":   func(a []any) (any, error) { return sp(ExcludePatternError(a[0])), nil },
	"isExcluded": func(a []any) (any, error) {
		return IsExcluded(a[1].(string), a[2].(bool), CompileExcludes(strsOf(a[0]))), nil
	},
	"normalizeVolumeTree": func(a []any) (any, error) { return NormalizeVolumeTree(a[0]) },
	"volumeTreeHashInput": func(a []any) (any, error) { return VolumeTreeHashInput(a[0]) },
	"treeStats":           func(a []any) (any, error) { return TreeStats(a[0].(*doc.Map)), nil },
	"treeNodes":           func(a []any) (any, error) { return TreeNodes(a[0].(*doc.Map)), nil },
	"filterExcluded":      func(a []any) (any, error) { return FilterExcluded(a[0].(*doc.Map), strsOf(a[1])), nil },
	"treeHasFile":         func(a []any) (any, error) { return TreeHasFile(a[0].(*doc.Map), a[1].(string)), nil },
	"treeHasDir":          func(a []any) (any, error) { return TreeHasDir(a[0].(*doc.Map), a[1].(string)), nil },
	"treeDelete":          func(a []any) (any, error) { return TreeDelete(a[0].(*doc.Map), a[1].(string)), nil },
	"duplicateName":       func(a []any) (any, error) { return DuplicateName(a[0].(*doc.Map), a[1].(string)), nil },
	"treeDuplicate": func(a []any) (any, error) {
		to, _ := opt(a, 2).(string)
		return TreeDuplicate(a[0].(*doc.Map), a[1].(string), to)
	},
	"treeMkdir": func(a []any) (any, error) { return TreeMkdir(a[0].(*doc.Map), a[1].(string)) },
	"treeMove":  func(a []any) (any, error) { return TreeMove(a[0].(*doc.Map), a[1].(string), a[2].(string)) },
	"treePutFile": func(a []any) (any, error) {
		return TreePutFile(a[0].(*doc.Map), a[1].(*doc.Map), mapOf(opt(a, 2)).Bool("overwrite"))
	},
	"volumeTreeIssues": func(a []any) (any, error) { return VolumeTreeIssues(a[0], a[1].(bool)) },
	"planVolumePush":   func(a []any) (any, error) { return PlanVolumePush(mapOf(a[0]), mapOf(a[1]), mapOf(a[2])), nil },
	"diffVolumeTrees":  func(a []any) (any, error) { return DiffVolumeTrees(mapOf(a[0]), mapOf(a[1])), nil },
	"countTreeChanges": func(a []any) (any, error) { return CountTreeChanges(mapOf(a[0]), mapOf(a[1])), nil },
	"mergeVolumePush": func(a []any) (any, error) {
		return MergeVolumePush(mapOf(a[0]), mapOf(a[1]), mapOf(a[2]), a[3].(string), mapOf(opt(a, 4)))
	},
	"languageForPath":    func(a []any) (any, error) { return sp(LanguageForPath(a[0].(string))), nil },
	"formatMode":         func(a []any) (any, error) { return FormatMode(a[0].(string)), nil },
	"formatContentBytes": func(a []any) (any, error) { return FormatContentBytes(a[0].(float64)), nil },
	"looksLikeText": func(a []any) (any, error) {
		b := []byte{}
		for _, x := range a[0].([]any) {
			b = append(b, byte(x.(float64)))
		}
		return LooksLikeText(b), nil
	},
	"volumePersist":        func(a []any) (any, error) { return VolumePersist(a[0]), nil },
	"usesVolumeContent":    func(a []any) (any, error) { return UsesVolumeContent(a[0]) },
	"isLegacyFileSeed":     func(a []any) (any, error) { return IsLegacyFileSeed(a[0]), nil },
	"defaultVolumePersist": func(a []any) (any, error) { return DefaultVolumePersist(), nil },
	"parentDir":            func(a []any) (any, error) { return ParentDir(a[0].(string)), nil },
	"baseName":             func(a []any) (any, error) { return BaseName(a[0].(string)), nil },
	"ancestorDirs":         func(a []any) (any, error) { return AncestorDirs(a[0].(string)), nil },
	"joinPath":             func(a []any) (any, error) { return JoinPath(a[0].(string), a[1].(string)), nil },
	"cloudSyncState":       func(a []any) (any, error) { return CloudSyncState(a[0].(*doc.Map)), nil },
	"migrateVolumeFileSeeds": func(a []any) (any, error) {
		known := a[1].(bool)
		return MigrateVolumeFileSeeds(a[0].(*doc.Map), func(_, v string) []any {
			if known && v == "v1" {
				return []any{doc.Obj("name", "known", "mountPath", "/data/k"), doc.Obj("name", "iv", "mountPath", "/iv")}
			}
			return nil
		})
	},
	"slugifyLabel": func(a []any) (any, error) { return SlugifyLabel(a[0].(string), a[1].(string)), nil },
	"uniqueSlug": func(a []any) (any, error) {
		taken := map[string]bool{}
		for _, s := range strsOf(a[1]) {
			taken[s] = true
		}
		return UniqueSlug(a[0].(string), taken), nil
	},
}

func undefArgs(a []any) []any {
	out := make([]any, len(a))
	for i, x := range a {
		out[i] = x
		if m, ok := x.(*doc.Map); ok && m.Len() == 1 && m.Bool("$undef") {
			out[i] = doc.Undefined
		}
	}
	return out
}

func short(s string) string {
	if len(s) > 900 {
		return s[:900] + "…"
	}
	return s
}

func TestVectors(t *testing.T) {
	v, err := doc.ParseMap(tsxRun(t, "testdata/vectors.mts"))
	if err != nil {
		t.Fatal(err)
	}
	fns := v.Map("fns")
	total, bad := 0, 0
	for _, name := range fns.Keys() {
		fn := goFns[name]
		if fn == nil {
			t.Errorf("no Go function for vector %s", name)
			continue
		}
		for _, c := range fns.Arr(name) {
			total++
			m := c.(*doc.Map)
			args := undefArgs(m.Arr("args"))
			out, err := func() (o any, e error) {
				defer func() {
					if r := recover(); r != nil {
						e = fmt.Errorf("PANIC %v", r)
					}
				}()
				return fn(args)
			}()
			var got, want string
			if err != nil {
				got = "throw: " + err.Error()
			} else {
				got = doc.MarshalString(out)
			}
			if m.Has("throw") {
				want = "throw: " + m.Str("throw")
			} else {
				want = doc.MarshalString(m.Val("out"))
			}
			if got != want {
				bad++
				if bad <= 40 {
					t.Errorf("%s(%s)\n got %s\nwant %s", name, short(doc.MarshalString(m.Val("args"))), short(got), short(want))
				}
			}
		}
	}
	t.Logf("%d vectors in %d functions, %d mismatches", total, fns.Len(), bad)
	if bad > 40 {
		t.Errorf("%d mismatches in total", bad)
	}
}

// Every constant and table against TS.
func TestValues(t *testing.T) {
	v, err := doc.ParseMap(tsxRun(t, "testdata/vectors.mts"))
	if err != nil {
		t.Fatal(err)
	}
	limits := doc.New()
	rl := reflect.ValueOf(LIMITS)
	for i := 0; i < rl.NumField(); i++ {
		n := rl.Type().Field(i).Name
		limits.Set(strings.ToLower(n[:1])+n[1:], rl.Field(i).Interface())
	}
	vcl := doc.New()
	rv := reflect.ValueOf(VOLUME_CONTENT_LIMITS)
	for i := 0; i < rv.NumField(); i++ {
		n := rv.Type().Field(i).Name
		vcl.Set(strings.ToLower(n[:1])+n[1:], rv.Field(i).Interface())
	}
	ops := doc.New()
	for k, o := range AUDIT_OPS {
		ops.Set(k, doc.Obj("action", o.Action, "targetType", o.TargetType))
	}
	sorted := func(m map[string]any) *doc.Map { x, _ := doc.ToMap(m); return x }
	status := map[string]any{}
	for k, n := range ERROR_HTTP_STATUS {
		status[k] = n
	}
	labels := map[string]any{}
	for k, n := range VOLUME_SYNC_STATE_LABEL {
		labels[k] = n
	}
	goVals := map[string]any{
		"MASK": MASK, "REVEAL_SECONDS": REVEAL_SECONDS, "REVEAL_RATE_PER_MINUTE": REVEAL_RATE_PER_MINUTE, "SEED_ARCHIVE_MAX": SEED_ARCHIVE_MAX, "GUI_UPGRADE_MIN_VERSION": GUI_UPGRADE_MIN_VERSION,
		"AGENT_UPGRADE_HEALTH_SEC": AGENT_UPGRADE_HEALTH_SEC, "AGENT_UPGRADE_REQUEST_TTL_SEC": AGENT_UPGRADE_REQUEST_TTL_SEC, "UPLOAD_PART_SIZE": UPLOAD_PART_SIZE, "SMALL_BLOB_MAX": SMALL_BLOB_MAX,
		"BLOB_STREAM_MAX": BLOB_STREAM_MAX, "TRANSFER_MAX": TRANSFER_MAX, "INTENT_MAX_SPEED": INTENT_MAX_SPEED, "INTENT_SPEED_HEARTBEAT_MS": INTENT_SPEED_HEARTBEAT_MS, "INTENT_SPEED_DECAY_MS": INTENT_SPEED_DECAY_MS,
		"INTENT_SPEED_X4_AFTER_MS": INTENT_SPEED_X4_AFTER_MS, "INTENT_SPEED_EASE_MS": INTENT_SPEED_EASE_MS, "ICON_MAX_BYTES": ICON_MAX_BYTES, "CLOCK_SKEW_WARN_SEC": CLOCK_SKEW_WARN_SEC, "AGENT_LEGACY_SCHEMA": AGENT_LEGACY_SCHEMA,
		"VOLUME_COPIES_DIR": VOLUME_COPIES_DIR, "VOLUME_OP_RATE_PER_MINUTE": VOLUME_OP_RATE_PER_MINUTE, "VOLUME_RESTORE_UPLOAD_MAX": VOLUME_RESTORE_UPLOAD_MAX, "HOST_FS_MAX_ENTRIES": HOST_FS_MAX_ENTRIES,
		"VOLUME_BACKUP_SCHEDULES": VOLUME_BACKUP_SCHEDULES, "DEFAULT_VOLUME_BACKUP_POLICY": DEFAULT_VOLUME_BACKUP_POLICY(), "PROPOSAL_TERMINAL": PROPOSAL_TERMINAL, "INSTALL_TOKEN_KINDS": INSTALL_TOKEN_KINDS, "INTENT_SPEEDS": INTENT_SPEEDS,
		"ICON_CONTENT_TYPES": ICON_CONTENT_TYPES, "AUDIT_TARGET_TYPES": AUDIT_TARGET_TYPES, "AUDIT_ACTIONS": AUDIT_ACTIONS, "AUDIT_DEPLOYMENT_OPS": AUDIT_DEPLOYMENT_OPS, "CREDENTIAL_OPS": CREDENTIAL_OPS, "AUDIT_EXEMPT_OPS": AUDIT_EXEMPT_OPS,
		"ACTIVITY_TYPES": ACTIVITY_TYPES, "AUDIT_OPS": ops, "ERROR_HTTP_STATUS": sorted(status),
		"STAGE_TTL_MS": STAGE_TTL_MS, "STAGE_MAX_BLOBS": STAGE_MAX_BLOBS, "STAGE_MAX_BYTES": STAGE_MAX_BYTES, "STAGING_PREFIX": STAGING_PREFIX,
		"FILE_MAX": FILE_MAX, "FILE_NOTE_MAX": FILE_NOTE_MAX, "FILE_TOMBSTONE_DAYS": FILE_TOMBSTONE_DAYS, "HOST_FS_PSEUDO": HOST_FS_PSEUDO, "HOST_FS_SECRETS": HOST_FS_SECRETS,
		"VOLUME_CONTENT_LIMITS": vcl, "VOLUME_SEED_MARKER": VOLUME_SEED_MARKER, "VOLUME_CONTENT_MIN_AGENT": VOLUME_CONTENT_MIN_AGENT, "VOLUME_CONTENT_COMMIT_MIN_AGENT": VOLUME_CONTENT_COMMIT_MIN_AGENT,
		"VOLUME_SYNC_SCAN_SEC": VOLUME_SYNC_SCAN_SEC, "VOLUME_SYNC_QUIET_SEC": VOLUME_SYNC_QUIET_SEC, "VOLUME_SYNC_MIN_INTERVAL_SEC": VOLUME_SYNC_MIN_INTERVAL_SEC, "VOLUME_CONTENT_APPLY_WAIT_SEC": VOLUME_CONTENT_APPLY_WAIT_SEC,
		"DEFAULT_FILE_MODE": DEFAULT_FILE_MODE, "VOLUME_ALWAYS_EXCLUDED": VOLUME_ALWAYS_EXCLUDED, "VOLUME_PERSIST_DEFAULT_EXCLUDE": VOLUME_PERSIST_DEFAULT_EXCLUDE, "VOLUME_SYNC_STATE_LABEL": sorted(labels),
		"EMPTY_VOLUME_TREE": EMPTY_VOLUME_TREE(), "IMAGE_IDENTITY_FIELDS": IMAGE_IDENTITY_FIELDS, "IMAGE_EDITABLE_FIELDS": IMAGE_EDITABLE_FIELDS, "IMAGE_NAME_KINDS": IMAGE_NAME_KINDS,
		"LIMITS": limits, "RESERVED_SITE_SLUGS": RESERVED_SITE_SLUGS, "RESERVED_CONTAINER_SLUGS": RESERVED_CONTAINER_SLUGS, "EDGE_METHODS": EDGE_METHODS,
	}
	vals := v.Map("vals")
	for _, k := range vals.Keys() {
		g, ok := goVals[k]
		if !ok {
			t.Errorf("%s: no Go value", k)
			continue
		}
		want := vals.Val(k)
		if wm, ok := want.(*doc.Map); ok && k != "DEFAULT_VOLUME_BACKUP_POLICY" && k != "EMPTY_VOLUME_TREE" {
			a, b := map[string]string{}, map[string]string{}
			wm.Each(func(k string, v any) { a[k] = doc.MarshalString(v) })
			g.(*doc.Map).Each(func(k string, v any) { b[k] = doc.MarshalString(v) })
			if !reflect.DeepEqual(a, b) {
				t.Errorf("%s\n got %v\nwant %v", k, b, a)
			}
			continue
		}
		if doc.MarshalString(g) != doc.MarshalString(want) {
			t.Errorf("%s\n got %s\nwant %s", k, doc.MarshalString(g), doc.MarshalString(want))
		}
	}
	// RE: every pattern against JS on the tricky strings
	rr := reflect.ValueOf(RE)
	names := map[string]string{"ID": "id", "ULID": "ulid", "E164": "e164", "IPv4": "ipv4", "HTTPSURL": "httpsUrl", "HTTPSURLAny": "httpsUrlAny", "DNSName": "dnsName", "EnvName": "envName", "RouteID": "routeId", "HeaderName": "headerName", "ActivityType": "activityType", "VolumeSlug": "volumeSlug"}
	reVals := v.Map("reVals")
	for i := 0; i < rr.NumField(); i++ {
		f := rr.Type().Field(i).Name
		k := names[f]
		if k == "" {
			k = strings.ToLower(f)
		}
		m := reVals.Map(k)
		if m == nil {
			t.Errorf("RE.%s has no TS twin", f)
			continue
		}
		re := rr.Field(i).MethodByName("MatchString")
		m.Each(func(s string, want any) {
			if got := re.Call([]reflect.Value{reflect.ValueOf(s)})[0].Bool(); got != want.(bool) {
				t.Errorf("RE.%s(%q) = %v", f, s, got)
			}
		})
	}
	if reVals.Len() != rr.NumField() {
		t.Errorf("RE: %d TS patterns, %d Go", reVals.Len(), rr.NumField())
	}
}
