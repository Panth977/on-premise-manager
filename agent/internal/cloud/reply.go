package cloud

// Reply is a non-JSON (or custom status / header) response: handlers return *Reply; everything else is JSON-encoded.
// Body: nil, string, []byte, or an io.Reader (closed after sending when it is an io.Closer).
type Reply struct {
	Status  int
	Headers map[string]string
	Body    any
}
