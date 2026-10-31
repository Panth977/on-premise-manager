package core

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"opm/agent/internal/cloud/doc"
)

// TarEntry is one file of a tar.
type TarEntry struct {
	Name string
	Data []byte
}

func tarStr(b []byte) string {
	if z := bytes.IndexByte(b, 0); z >= 0 {
		b = b[:z]
	}
	return Utf8(b)
}

// tarOctal is parseInt(trimmed, 8); ok=false for NaN.
func tarOctal(b []byte) (float64, bool) {
	s := doc.JSTrim(tarStr(b))
	if s == "" {
		return 0, true
	}
	neg := false
	if s[0] == '+' || s[0] == '-' {
		neg = s[0] == '-'
		s = s[1:]
	}
	n, i := 0.0, 0
	for ; i < len(s) && s[i] >= '0' && s[i] <= '7'; i++ {
		n = n*8 + float64(s[i]-'0')
	}
	if i == 0 {
		return 0, false
	}
	if neg {
		n = -n
	}
	return n, true
}

var (
	rePaxPath    = regexp.MustCompile(`^[0-9]+ path=([^\n\r\x{2028}\x{2029}]*)$`)
	reTrailingNu = regexp.MustCompile(`\x00+$`)
)

// ReadTar is readTar: regular files of a POSIX ustar (pax 'x'/'g' and GNU 'L' long names understood).
func ReadTar(buf []byte) ([]TarEntry, error) {
	out := []TarEntry{}
	off := 0
	longName, paxPath := "", ""
	zero := make([]byte, 512)
	for off+512 <= len(buf) {
		h := buf[off : off+512]
		if bytes.Equal(h, zero) {
			break
		}
		size, ok := tarOctal(h[124:136])
		if !ok || size < 0 || math.IsInf(size, 0) {
			return nil, errors.New("bad tar header (size)")
		}
		sum := 0
		for i, x := range h {
			if i >= 148 && i < 156 {
				sum += 32
			} else {
				sum += int(x)
			}
		}
		if want, ok := tarOctal(h[148:156]); !ok || float64(sum) != want {
			return nil, errors.New("bad tar header (checksum)")
		}
		typ := h[156]
		if typ == 0 {
			typ = '0'
		}
		name := tarStr(h[0:100])
		if strings.HasPrefix(tarStr(h[257:263]), "ustar") {
			if p := tarStr(h[345:500]); p != "" {
				name = p + "/" + name
			}
		}
		start := off + 512
		if float64(start)+size > float64(len(buf)) {
			return nil, errors.New("truncated tar")
		}
		data := buf[start : start+int(size)]
		off = start + int(math.Ceil(size/512))*512
		switch typ {
		case 'L':
			longName = reTrailingNu.ReplaceAllString(Utf8(data), "")
			continue
		case 'x':
			for _, line := range strings.Split(Utf8(data), "\n") {
				if m := rePaxPath.FindStringSubmatch(line); m != nil {
					paxPath = m[1]
				}
			}
			continue
		case 'g':
			continue
		}
		if longName != "" {
			name = longName
		}
		if paxPath != "" {
			name = paxPath
		}
		longName, paxPath = "", ""
		switch typ {
		case '0', '7':
			out = append(out, TarEntry{strings.TrimPrefix(name, "./"), append([]byte{}, data...)})
		case '5':
		default:
			return nil, fmt.Errorf("unsupported tar entry type %s (%s)", string(rune(typ)), name)
		}
	}
	return out, nil
}

// WriteTar is writeTar (tests, tools).
func WriteTar(entries []TarEntry) ([]byte, error) {
	var out bytes.Buffer
	for _, e := range entries {
		h := make([]byte, 512)
		if len(e.Name) > 100 {
			return nil, errors.New("name too long")
		}
		copy(h, e.Name)
		copy(h[100:], "0000644\x00")
		copy(h[108:], "0000000\x00")
		copy(h[116:], "0000000\x00")
		copy(h[124:], fmt.Sprintf("%011o\x00", len(e.Data)))
		copy(h[136:], fmt.Sprintf("%011o\x00", time.Now().Unix()))
		copy(h[148:], "        ")
		h[156] = '0'
		copy(h[257:], "ustar\x00")
		copy(h[263:], "00")
		sum := 0
		for _, x := range h {
			sum += int(x)
		}
		copy(h[148:], fmt.Sprintf("%06o\x00 ", sum))
		out.Write(h)
		out.Write(e.Data)
		out.Write(make([]byte, (512-len(e.Data)%512)%512))
	}
	out.Write(make([]byte, 1024))
	return out.Bytes(), nil
}
