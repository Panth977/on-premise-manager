package core

import (
	_ "embed"
	"regexp"
)

// installScriptTmpl is the install script (edit it there): POSIX sh with @@OPM:<VAR>@@ placeholders, kept sh -n / dash -n /
// shellcheck clean (apitest 17_install_script).
//
//go:embed install_script.tmpl
var installScriptTmpl string

var reScriptVar = regexp.MustCompile(`@@OPM:([A-Z0-9_]+)@@`)

// RenderInstallScript is renderInstallScript of routes/install-script.ts; v: the InstallScriptVars (a missing one renders
// "undefined", as the template literal does).
func RenderInstallScript(v map[string]string) string {
	return reScriptVar.ReplaceAllStringFunc(installScriptTmpl, func(m string) string {
		if s, ok := v[m[6:len(m)-2]]; ok {
			return s
		}
		return "undefined"
	})
}
