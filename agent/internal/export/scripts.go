package export

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// The machine-side scripts of a site. All of them are plain POSIX sh + awk and run in official images only:
//   deploy.sh  (host)                 the one command: setup.sh in busybox, mosquitto_passwd in eclipse-mosquitto,
//                                     then docker compose up -d
//   setup.sh   (busybox, host user)   site.env → .runtime/v/<NAME>, derived values, templates → .runtime/, .env
//   init.sh    (busybox, compose)     seeds empty volumes once, installs the static busybox for the health checks

// EdgeTLSDir is where the edge container sees the TLS files rendered by setup.sh.
const EdgeTLSDir = "/etc/caddy/tls"

// seedFile / seedPlan: the starter content init.sh writes into one volume.
type seedFile struct {
	Path     string // inside the volume ("" = a single-file volume)
	Mode     os.FileMode
	Rendered bool // from .runtime/seed/ (rendered by setup.sh) instead of the git copy
}

type seedPlan struct {
	Volume   string
	FileKind bool
	DirPath  string // where init.sh sees a folder volume
	FilePath string // where init.sh sees a single-file volume
	Source   string // tree | single | snapshot | archive | "" (nothing: a single-file volume only gets its empty file)
	Files    []seedFile
	Dirs     []string
	Archive  string // git path of the .tar.gz (archive seeds)
	Skipped  int
}

func filesShaLines(st *siteState) string {
	var b strings.Builder
	for _, h := range st.hashes {
		b.WriteString(h.Name + "\t" + strings.Join(h.Paths, " ") + "\n")
	}
	return b.String()
}

// filesShaName is the .env name of a service's file hash.
func filesShaName(service string) string {
	return "FILES_SHA_" + strings.ToUpper(strings.NewReplacer("-", "_", ".", "_").Replace(service))
}

func oct(m os.FileMode) string { return fmt.Sprintf("%04o", uint32(m.Perm())) }

// awkLib: the awk functions setup.sh shares (values are files .runtime/v/<NAME>; whole files are one record).
const awkLib = `
function val(n,   f, v, rc) {
  f = D "/" n; v = ""; rc = (getline v < f)
  if (rc < 0) { printf "setup: %s has no value (site.env)\n", n > "/dev/stderr"; exit 3 }
  close(f); return v
}
function jesc(v,   o, i, c) {
  o = ""
  for (i = 1; i <= length(v); i++) {
    c = substr(v, i, 1)
    if (c == "\\") o = o "\\\\"; else if (c == "\"") o = o "\\\""
    else if (c == "\n") o = o "\\n"; else if (c == "\r") o = o "\\r"; else if (c == "\t") o = o "\\t"
    else if (c == "\b") o = o "\\b"; else if (c == "\f") o = o "\\f"
    else if (c in CTL) o = o sprintf("\\u%04x", CTL[c]); else o = o c
  }
  return o
}
function denv(v,   o, i, c) {
  o = ""
  for (i = 1; i <= length(v); i++) {
    c = substr(v, i, 1)
    if (c == "\\") o = o "\\\\"; else if (c == "\"") o = o "\\\""; else if (c == "$") o = o "$$"
    else if (c == "\n") o = o "\\n"; else if (c == "\r") o = o "\\r"; else o = o c
  }
  return o
}
BEGIN { RS = "\001"; ORS = ""; for (i = 1; i < 32; i++) CTL[sprintf("%c", i)] = i }
`

// setupScript renders sites/<site>/setup.sh.
func (x *exporter) setupScript(st *siteState, required []deferredInput, steps []*step) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# Site %s: machine-side setup (generated with %s @ %s). deploy.sh runs it in the official busybox image:\n", st.site, x.req.Branch.GitBranch, x.req.Commit.ID)
	b.WriteString("#   sh setup.sh values   site.env → .runtime/v/<NAME>; checks every required value\n")
	b.WriteString("#   sh setup.sh steps    computes the values derived from them (base64, sha256, ...); the mosquitto_passwd ones are\n")
	b.WriteString("#                        listed in .runtime/pending for deploy.sh (official eclipse-mosquitto image)\n")
	b.WriteString("#   sh setup.sh render   writes .env (docker compose reads it) and the files under .runtime/\n")
	b.WriteString("set -eu\nR=.runtime\nD=$R/v\n")
	var req []string
	for _, d := range required {
		req = append(req, d.Name)
	}
	b.WriteString("REQUIRED=" + shq(strings.Join(req, " ")) + "\n")
	b.WriteString("LIB=" + shq(awkLib) + "\n")
	b.WriteString(`die() { echo "setup: $*" >&2; exit 1; }
off() { case "$(cat "$D/$1" 2>/dev/null || true)" in off|false|no|0) return 0 ;; esac; return 1; }

# tpl SRC DST: @@{NAME}@@ → the value of NAME, @@{NAME|json}@@ → the same escaped for a JSON string
tpl() {
  awk -v D="$D" "$LIB"'
  { s = $0; out = ""
    while ((i = index(s, "@@{")) > 0) {
      j = index(substr(s, i + 3), "}@@"); if (j == 0) break
      tok = substr(s, i + 3, j - 1); out = out substr(s, 1, i - 1); s = substr(s, i + j + 5)
      n = tok; k = index(tok, "|"); if (k > 0) n = substr(tok, 1, k - 1)
      if (n !~ /^[A-Z0-9_]+$/) { out = out "@@{" tok "}@@"; continue }
      v = val(n); if (k > 0 && substr(tok, k + 1) == "json") v = jesc(v)
      out = out v
    }
    printf "%s", out s }' "$1" > "$2.tmp"
  mv "$2.tmp" "$2"
}

# put SRC DST MODE: an unchanged DST is left alone; a changed one is replaced (rename: whoever owns the old file). The
# containers that mount a changed file are recreated by docker compose (label files.sha256), like the agent restarts them.
put() {
  mkdir -p "$(dirname "$2")"
  if [ -d "$2" ]; then rmdir "$2" 2>/dev/null || die "$2 is a folder"; fi
  if [ -f "$2" ] && cmp -s "$1" "$2"; then chmod "$3" "$2" 2>/dev/null || true; return 0; fi
  cp "$1" "$2.new"
  chmod "$3" "$2.new"
  mv -f "$2.new" "$2"
}

values() {
  [ -f site.env ] || die "site.env is missing: cp site.env.example site.env and fill it"
  mkdir -p "$D" "$R/in"
  # the values of site.env are read again; derived values stay (their input hash decides when they change)
  for f in "$D"/*; do case "${f##*/}" in OPM_D_*|\*) ;; *) rm -f "$f" ;; esac; done
  awk -v d="$D" '
    { sub(/\r$/, "") }
    /^[ \t]*(#|$)/ { next }
    { i = index($0, "=")
      if (i < 2) { printf "site.env line %d: expected NAME=value\n", NR > "/dev/stderr"; bad = 1; next }
      n = substr($0, 1, i - 1); v = substr($0, i + 1)
      sub(/^[ \t]*(export[ \t]+)?/, "", n); sub(/[ \t]+$/, "", n)
      if (n !~ /^[A-Za-z_][A-Za-z0-9_]*$/) { printf "site.env line %d: %s is not a valid name\n", NR, n > "/dev/stderr"; bad = 1; next }
      q = substr(v, 1, 1)
      if (length(v) >= 2 && (q == "\"" || q == "'\''") && substr(v, length(v)) == q) v = substr(v, 2, length(v) - 2)
      f = d "/" n; printf "%s", v > f; close(f) }
    END { exit bad }' site.env
  missing=''
  for n in $REQUIRED; do [ -s "$D/$n" ] || missing="$missing $n"; done
  if ! off PORTAINER; then
    [ -s "$D/PORTAINER_ADMIN_PASSWORD" ] || missing="$missing PORTAINER_ADMIN_PASSWORD"
  fi
  [ -z "$missing" ] || die "set these values in site.env (see site.env.example):$missing"
  if ! off PORTAINER && [ "$(wc -c < "$D/PORTAINER_ADMIN_PASSWORD")" -lt 12 ]; then
    die "PORTAINER_ADMIN_PASSWORD needs 12 characters or more (or set PORTAINER=off)"
  fi
  echo "setup: site.env read"
}

# one derived value: step NAME FILTER [ARGS]; the input is templates/steps/NAME.in rendered
step() {
  n=$1 f=$2 in=$R/in/$1
  tpl "templates/steps/$n.in" "$in" 2>/dev/null || return 0 # needs a value computed later (next round)
  s=$(sha256sum < "$in" | cut -c1-64)
  if [ -f "$D/$n" ] && [ "$(cat "$in.sha" 2>/dev/null || true)" = "$s" ]; then return 0; fi
  o=$D/$n.tmp
  case "$f" in
    upper) tr a-z A-Z < "$in" > "$o" ;;
    lower) tr A-Z a-z < "$in" > "$o" ;;
    trim) awk "$LIB"'{ sub(/^[ \t\n\r\v\f]+/, ""); sub(/[ \t\n\r\v\f]+$/, ""); printf "%s", $0 }' "$in" > "$o" ;;
    base64) base64 -w 0 < "$in" | tr -d '\n' > "$o" ;;
    url) od -An -v -tx1 < "$in" | awk '{ for (i = 1; i <= NF; i++) { h = $i; c = (index("0123456789abcdef", substr(h, 1, 1)) - 1) * 16 + index("0123456789abcdef", substr(h, 2, 1)) - 1
           if ((c >= 65 && c <= 90) || (c >= 97 && c <= 122) || (c >= 48 && c <= 57) || c == 45 || c == 95 || c == 46 || c == 126) printf "%c", c; else printf "%%%s", toupper(h) } }' > "$o" ;;
    sha256) sha256sum < "$in" | cut -c1-64 | tr -d '\n' > "$o" ;;
    sha512) sha512sum < "$in" | cut -c1-128 | tr -d '\n' > "$o" ;;
    int) awk "$LIB"'{ v = $0 } END { if (v !~ /^[ \t\n]*-?[0-9]+(\.[0-9]+)?[ \t\n]*$/) { print "setup: \"" v "\" is not an integer\n" > "/dev/stderr"; exit 1 }
           x = int(v + 0); if (x == 0) x = 0; printf "%d", x }' "$in" > "$o" ;;
    json) awk "$LIB"'{ v = $0 } END { printf "\"%s\"", jesc(v) }' "$in" > "$o" ;;
    mosquitto_passwd)
      if [ -f "$D/$n.new" ]; then mv "$D/$n.new" "$D/$n"; printf %s "$s" > "$in.sha"; else rm -f "$D/$n"; echo "$n $3 $4" >> "$R/pending"; fi
      return 0 ;;
    *) die "unknown step $f" ;;
  esac
  mv "$o" "$D/$n"
  printf %s "$s" > "$in.sha"
}

steps() {
  : > "$R/pending"
`)
	for _, s := range steps {
		args := ""
		if s.Filter == "mosquitto_passwd" {
			format, iterations := 7, 101
			if len(s.Args) >= 1 {
				if f, ok := s.Args[0].(float64); ok {
					format = int(f)
				}
			}
			if len(s.Args) == 2 {
				if f, ok := s.Args[1].(float64); ok {
					iterations = int(f)
				}
			}
			h := "sha512-pbkdf2"
			if format == 6 {
				h = "sha512"
			}
			args = " " + h + " " + strconv.Itoa(iterations)
		}
		b.WriteString("  step " + s.Name + " " + s.Filter + args + "\n")
	}
	if len(steps) == 0 {
		b.WriteString("  : # this site has no derived values\n")
	}
	b.WriteString("}\n\nrender() {\n  mkdir -p \"$R/files\" \"$R/secrets\" \"$R/tls\" \"$R/edge\" \"$R/seed\" .data\n")
	b.WriteString("  # kind (t = template, c = copy) · source in git · destination under .runtime/ · mode (tab-separated)\n")
	b.WriteString("  while IFS='\t' read -r k src dst mode; do\n")
	b.WriteString("    [ -n \"$k\" ] || continue\n")
	b.WriteString("    if [ \"$k\" = t ]; then tpl \"$src\" \"$R/tmp\"; else cp \"$src\" \"$R/tmp\"; fi\n")
	b.WriteString("    put \"$R/tmp\" \"$R/$dst\" \"$mode\"\n")
	b.WriteString("  done <<'EOF'\n")
	for _, in := range st.installs {
		k := "c"
		if in.Template {
			k = "t"
		}
		b.WriteString(k + "\t" + in.Src + "\t" + in.Dst + "\t" + oct(in.Mode) + "\n")
	}
	b.WriteString("EOF\n")
	b.WriteString(`  rm -f "$R/tmp"
  # Portainer's admin password (read once, at Portainer's first start)
  if off PORTAINER; then : > "$R/tmp"; else cp "$D/PORTAINER_ADMIN_PASSWORD" "$R/tmp"; fi
  put "$R/tmp" "$R/portainer-admin-password" 0400
  rm -f "$R/tmp"
  # the hash of the files each service mounts (label files.sha256: a change recreates that service)
  while IFS='	' read -r n paths; do
    [ -n "$n" ] || continue
    # shellcheck disable=SC2086 # paths: space-separated, no spaces inside
    cat $paths | sha256sum | cut -c1-64 | tr -d '\n' > "$D/$n"
  done <<'EOF'
` + filesShaLines(st) + `EOF
  # .env: every value for docker compose (double-quoted: compose reads it back byte for byte)
  {
    echo "# generated by ./deploy.sh from site.env: do not edit (edit site.env, then run ./deploy.sh)"
    for f in "$D"/*; do
      [ -f "$f" ] || continue
      n=${f##*/}
      case "$n" in *.tmp|*.new|PORTAINER) continue ;; esac
      printf '%s="' "$n"; awk -v D="$D" "$LIB"'{ printf "%s", denv($0) }' "$f"; echo '"'
    done
    if off PORTAINER; then echo 'COMPOSE_PROFILES=""'; else echo 'COMPOSE_PROFILES="portainer"'; fi
  } > .env.tmp
  chmod 600 .env.tmp
  mv .env.tmp .env
  echo "setup: .env and .runtime/ written"
}

case "${1:-}" in
  values) values ;;
  steps) steps ;;
  render) render ;;
  *) die "usage: sh setup.sh values|steps|render (deploy.sh runs them)" ;;
esac
`)
	return b.String()
}

// initScript renders sites/<site>/init.sh (the init service).
func (x *exporter) initScript(st *siteState) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# Site %s: the init service (generated with %s @ %s). docker compose runs it before every container:\n", st.site, x.req.Branch.GitBranch, x.req.Commit.ID)
	b.WriteString("# installs the static busybox the health checks use, then seeds every EMPTY volume once with its starter content\n")
	b.WriteString("# (marker .opm-seeded: a second `up` never re-seeds, like the agent).\nset -eu\n")
	b.WriteString("cp /bin/busybox /tools/busybox.new && chmod 0755 /tools/busybox.new && mv /tools/busybox.new /tools/busybox\n")
	b.WriteString(`marker() { printf '{"at":"%s","source":"%s","entries":%d}\n' "$(date -u +%Y-%m-%dT%H:%M:%S.000Z)" "$2" "$3" > "$1"; }
`)
	for _, p := range st.seeds {
		if p.FileKind {
			if p.FilePath == "" {
				continue
			}
			fmt.Fprintf(&b, "\n# volume %s (a single file)\nf=%s\nmkdir -p \"$(dirname \"$f\")\"\n", p.Volume, shq(p.FilePath))
			b.WriteString("if [ -d \"$f\" ]; then rmdir \"$f\" 2>/dev/null || { echo \"init: $f is a folder, the volume is a single file\" >&2; exit 1; }; fi\n")
			b.WriteString("[ -e \"$f\" ] || : > \"$f\"\n")
			if p.Source == "" || len(p.Files) == 0 {
				continue
			}
			src := "/site/volumes/" + p.Volume
			if p.Files[0].Rendered {
				src = "/runtime/seed/" + p.Volume
			}
			m := "/data/.opm-seeded/" + p.Volume
			fmt.Fprintf(&b, "if [ ! -e %s ] && [ ! -s \"$f\" ]; then\n  cat %s > \"$f\"\n  chmod %s \"$f\"\n  mkdir -p /data/.opm-seeded\n  marker %s %s 1\n  echo \"init: volume %s seeded (1 file)\"\nfi\n",
				shq(m), shq(src), oct(p.Files[0].Mode), shq(m), p.Source, p.Volume)
			continue
		}
		if p.DirPath == "" || p.Source == "" {
			continue
		}
		fmt.Fprintf(&b, "\n# volume %s\nd=%s\nmkdir -p \"$d\"\n", p.Volume, shq(p.DirPath))
		b.WriteString("if [ ! -e \"$d/.opm-seeded\" ] && [ -z \"$(ls -A \"$d\")\" ]; then\n")
		n := len(p.Files)
		if p.Archive != "" {
			fmt.Fprintf(&b, "  tar -xzf %s -C \"$d\"\n", shq("/site/"+p.Archive))
		}
		var git, rendered bool
		for _, f := range p.Files {
			if f.Rendered {
				rendered = true
			} else {
				git = true
			}
		}
		if git {
			fmt.Fprintf(&b, "  cp -R %s \"$d/\"\n", shq("/site/volumes/"+p.Volume+"/."))
		}
		if rendered {
			fmt.Fprintf(&b, "  cp -R %s \"$d/\"\n", shq("/runtime/seed/"+p.Volume+"/."))
		}
		for _, dd := range p.Dirs {
			fmt.Fprintf(&b, "  mkdir -p \"$d\"/%s\n", shq(dd))
		}
		if n > 0 {
			b.WriteString("  while IFS='\t' read -r m p; do chmod \"$m\" \"$d/$p\"; done <<'EOF'\n")
			for _, f := range p.Files {
				b.WriteString(oct(f.Mode) + "\t" + f.Path + "\n")
			}
			b.WriteString("EOF\n")
		}
		if p.Archive != "" {
			b.WriteString("  n=$(find \"$d\" -type f | wc -l)\n")
		} else {
			fmt.Fprintf(&b, "  n=%d\n", n)
		}
		src := p.Source
		if src == "snapshot" {
			src = "tree"
		}
		fmt.Fprintf(&b, "  marker \"$d/.opm-seeded\" %s \"$n\"\n  echo \"init: volume %s seeded ($n file(s))\"\n", src, p.Volume)
		if p.Skipped > 0 {
			fmt.Fprintf(&b, "  echo \"init: volume %s: %d file(s) larger than 100 MB are not in git and were not written\"\n", p.Volume, p.Skipped)
		}
		b.WriteString("fi\n")
	}
	b.WriteString("\necho \"init: done\"\n")
	return b.String()
}

// deployScript is sites/<site>/deploy.sh (the same for every site).
var deployScript = `#!/bin/sh
# Deploys this site with Docker only (no agent):
#   1. setup.sh in the official busybox image: reads site.env, computes the derived values, writes .env + .runtime/
#   2. the mosquitto_passwd values in the official eclipse-mosquitto image (only when the config uses that filter)
#   3. docker compose up -d   (extra arguments are passed on, e.g. ./deploy.sh --force-recreate)
# ./deploy.sh --render-only stops after step 2 (check .env and .runtime/ without touching the running site).
# Run it again after every change: git merge from the cloud branch, a new site.env value, a local edit.
set -eu
cd "$(dirname "$0")"
BUSYBOX='` + InitImage + `'
MOSQUITTO='` + MosquittoImage + `'
if docker compose version >/dev/null 2>&1; then DC="docker compose"
elif command -v docker-compose >/dev/null 2>&1; then DC=docker-compose
else echo "deploy: Docker Compose v2 is needed (docker compose version)" >&2; exit 1; fi
[ -f site.env ] || { echo "deploy: site.env is missing: cp site.env.example site.env, fill it, run ./deploy.sh again" >&2; exit 1; }
U="$(id -u):$(id -g)"
mkdir -p .runtime .data
bb() { docker run --rm --network none --user "$U" -v "$PWD:/site" -w /site "$BUSYBOX" sh ./setup.sh "$1"; }
bb values
bb steps
rounds=0
while [ -s .runtime/pending ]; do
  while read -r n h it; do
    [ -n "$n" ] || continue
    docker run --rm --network none --user "$U" -v "$PWD/.runtime:/r" --entrypoint sh "$MOSQUITTO" -c '
      set -e; f=$(mktemp); [ "$2" = sha512 ] && set -- "$1" "$2" "" || set -- "$1" "$2" "-I $3"
      mosquitto_passwd -H "$2" $3 -b -c "$f" u "$(cat "/r/in/$1")"
      sed -n "s/^u://p" "$f" | tr -d "\n" > "/r/v/$1.new"; rm -f "$f"' sh "$n" "$h" "$it"
  done < .runtime/pending
  bb steps
  rounds=$((rounds + 1))
  [ "$rounds" -lt 5 ] || { echo "deploy: the derived values do not settle" >&2; exit 1; }
done
bb render
if [ "${1:-}" = --render-only ]; then exit 0; fi
$DC up -d --remove-orphans "$@"
$DC ps
`
