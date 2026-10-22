package edgecfg

import (
	"regexp"
	"strings"
)

var pemBlock = regexp.MustCompile(`-----BEGIN ([A-Z0-9 ]+)-----(.*?)-----END ([A-Z0-9 ]+)-----`)

// RepairPEM (§21 #120) puts back the line breaks of PEM text whose breaks became spaces (a key pasted into a
// one-line secret field: "-----BEGIN RSA PRIVATE KEY----- MIIE… … -----END RSA PRIVATE KEY-----"). Text that already
// has line breaks, or is no PEM, is returned unchanged.
func RepairPEM(s string) string {
	if strings.Contains(s, "\n") || !strings.Contains(s, "-----BEGIN ") {
		return s
	}
	var b strings.Builder
	for _, m := range pemBlock.FindAllStringSubmatch(s, -1) {
		if m[1] != m[3] {
			return s
		}
		b.WriteString("-----BEGIN " + m[1] + "-----\n")
		for _, f := range strings.Fields(m[2]) {
			if strings.Contains(f, ":") { // a header such as Proc-Type: 4,ENCRYPTED keeps its own line
				b.WriteString(f + " ")
				continue
			}
			b.WriteString(f + "\n")
		}
		b.WriteString("-----END " + m[1] + "-----\n")
	}
	if b.Len() == 0 {
		return s
	}
	return b.String()
}
