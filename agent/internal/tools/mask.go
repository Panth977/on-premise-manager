package tools

import (
	"bytes"
	"sort"
)

// MaskText is what a secret value becomes in a transcript / inspect answer.
const MaskText = "••••••"

// masker replaces known secret values in a byte stream. A value split across two writes is still caught: the last
// (longest secret − 1) bytes are held back until more data arrives or Flush. Values shorter than 4 bytes are ignored
// (they would mask ordinary output).
type masker struct {
	secrets [][]byte
	maxLen  int
	carry   []byte
}

func newMasker(values []string) *masker {
	m := &masker{}
	seen := map[string]bool{}
	for _, v := range values {
		if len(v) < 4 || seen[v] {
			continue
		}
		seen[v] = true
		m.secrets = append(m.secrets, []byte(v))
	}
	// longest first: a secret that contains another is masked as a whole
	sort.Slice(m.secrets, func(i, j int) bool { return len(m.secrets[i]) > len(m.secrets[j]) })
	for _, s := range m.secrets {
		if len(s) > m.maxLen {
			m.maxLen = len(s)
		}
	}
	return m
}

// Write returns the bytes that are safe to emit now.
func (m *masker) Write(p []byte) []byte {
	if len(m.secrets) == 0 {
		return p
	}
	buf := append(m.carry, p...)
	cut := len(buf) - (m.maxLen - 1)
	if cut <= 0 {
		m.carry = buf
		return nil
	}
	out, consumed := m.scan(buf, cut)
	m.carry = append([]byte(nil), buf[consumed:]...)
	return out
}

// Flush emits the held-back bytes (masked).
func (m *masker) Flush() []byte {
	if len(m.carry) == 0 {
		return nil
	}
	out, _ := m.scan(m.carry, len(m.carry))
	m.carry = nil
	return out
}

// scan masks matches that START before limit; returns the output and how many input bytes it consumed.
func (m *masker) scan(buf []byte, limit int) ([]byte, int) {
	var out bytes.Buffer
	i := 0
	for i < limit {
		hit := 0
		for _, s := range m.secrets {
			if bytes.HasPrefix(buf[i:], s) {
				hit = len(s)
				break
			}
		}
		if hit > 0 {
			out.WriteString(MaskText)
			i += hit
			continue
		}
		out.WriteByte(buf[i])
		i++
	}
	return out.Bytes(), i
}

// MaskString masks every known value in s (inspect answers, netcheck output).
func MaskString(s string, values []string) string {
	m := newMasker(values)
	out := m.Write([]byte(s))
	return string(append(out, m.Flush()...))
}
