package driver

import (
	"os"
	"sync"

	"opm/agent/internal/cloud/doc"
)

// Log is the structured logger of log.ts: one JSON line per entry ({...fields, severity, message}); WARNING and ERROR
// go to stderr, the rest to stdout. Fields are key, value pairs.
var Log logger

type logger struct{}

var logMu sync.Mutex

func logOut(severity, msg string, kv []any) {
	m := doc.New()
	for i := 0; i+1 < len(kv); i += 2 {
		if k, ok := kv[i].(string); ok {
			v := kv[i+1]
			if e, isErr := v.(error); isErr {
				v = e.Error()
			}
			m.Set(k, v)
		}
	}
	m.Set("severity", severity).Set("message", msg)
	line, err := doc.Marshal(m)
	if err != nil {
		line, _ = doc.Marshal(doc.Obj("severity", severity, "message", msg, "fields", err.Error()))
	}
	line = append(line, '\n')
	logMu.Lock()
	defer logMu.Unlock()
	if severity == "ERROR" || severity == "WARNING" {
		_, _ = os.Stderr.Write(line)
	} else {
		_, _ = os.Stdout.Write(line)
	}
}

func (logger) Debug(msg string, kv ...any) {
	if os.Getenv("OPM_LOG_DEBUG") == "1" {
		logOut("DEBUG", msg, kv)
	}
}
func (logger) Info(msg string, kv ...any)  { logOut("INFO", msg, kv) }
func (logger) Warn(msg string, kv ...any)  { logOut("WARNING", msg, kv) }
func (logger) Error(msg string, kv ...any) { logOut("ERROR", msg, kv) }
