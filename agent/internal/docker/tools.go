package docker

// Engine API calls used by the operator toolbox (internal/tools, internal/metrics): raw inspect, restart, top,
// events, one-shot stats, streaming logs, interactive exec (hijacked TTY), images + system df. Every field used exists
// in API 1.41 (Docker 20.10, the floor).

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// InspectRaw returns GET /containers/{id}/json as raw JSON.
func (c *Client) InspectRaw(ctx context.Context, id string) (json.RawMessage, error) {
	sc := c.stateCache()
	var gen uint64
	if sc != nil {
		v, hit, g, live := sc.inspGet(id)
		if hit && v.raw != nil {
			return v.raw, nil // shared: callers only read it
		}
		if live {
			gen = g
		} else {
			sc = nil
		}
	}
	resp, err := c.do(ctx, "GET", "/containers/"+id+"/json", nil, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err == nil && sc != nil {
		var cj ContainerJSON
		if json.Unmarshal(b, &cj) == nil {
			sc.inspPut(id, gen, &cj, b)
		}
	}
	return json.RawMessage(b), err
}

// ContainerRestart restarts a container (timeoutSec < 0 = its own StopTimeout).
func (c *Client) ContainerRestart(ctx context.Context, id string, timeoutSec int) error {
	q := url.Values{}
	if timeoutSec >= 0 {
		q.Set("t", strconv.Itoa(timeoutSec))
	}
	return c.jsonDo(ctx, "POST", "/containers/"+id+"/restart", q, nil, nil)
}

// TopResult is GET /containers/{id}/top.
type TopResult struct {
	Titles    []string   `json:"Titles"`
	Processes [][]string `json:"Processes"`
}

// ContainerTop lists the processes of a running container (psArgs "" = Docker's default "-ef").
func (c *Client) ContainerTop(ctx context.Context, id, psArgs string) (*TopResult, error) {
	q := url.Values{}
	if psArgs != "" {
		q.Set("ps_args", psArgs)
	}
	var out TopResult
	if err := c.jsonDo(ctx, "GET", "/containers/"+id+"/top", q, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// Event is one item of GET /events.
type Event struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID         string            `json:"ID"`
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	Time     int64 `json:"time"`
	TimeNano int64 `json:"timeNano"`
}

// Events returns the events between since and until (until must be set: the call returns when it is reached).
func (c *Client) Events(ctx context.Context, since, until time.Time, filters map[string][]string) ([]Event, error) {
	q := url.Values{"since": {strconv.FormatInt(since.Unix(), 10)}, "until": {strconv.FormatInt(until.Unix(), 10)}}
	if len(filters) > 0 {
		f, _ := json.Marshal(filters)
		q.Set("filters", string(f))
	}
	resp, err := c.do(ctx, "GET", "/events", q, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	dec := json.NewDecoder(io.LimitReader(resp.Body, 32<<20))
	var out []Event
	for {
		var e Event
		if err := dec.Decode(&e); err != nil {
			if errors.Is(err, io.EOF) {
				return out, nil
			}
			return out, err
		}
		out = append(out, e)
	}
}

// StatsSample is the subset of GET /containers/{id}/stats the monitor uses (cumulative counters).
type StatsSample struct {
	Read     time.Time
	CPUTotal uint64 // ns of CPU used by the container (cumulative)
	System   uint64 // ns of host CPU (cumulative; 0 on some platforms)
	CPUs     int
	MemUsage uint64 // usage - inactive_file (what docker stats shows)
	MemLimit uint64
	NetRx    uint64
	NetTx    uint64
	BlkRead  uint64
	BlkWrite uint64
	Pids     uint64
}

type statsWire struct {
	Read     string `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage  uint64   `json:"total_usage"`
			PercpuUsage []uint64 `json:"percpu_usage"`
		} `json:"cpu_usage"`
		SystemUsage uint64 `json:"system_cpu_usage"`
		OnlineCPUs  int    `json:"online_cpus"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
	PidsStats struct {
		Current uint64 `json:"current"`
	} `json:"pids_stats"`
}

// StatsOneShot reads one stats sample without waiting for a second one (one-shot=1, API ≥ 1.41).
func (c *Client) StatsOneShot(ctx context.Context, id string) (*StatsSample, error) {
	resp, err := c.do(ctx, "GET", "/containers/"+id+"/stats", url.Values{"stream": {"0"}, "one-shot": {"1"}}, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	var s statsWire
	if err := json.NewDecoder(bufio.NewReader(resp.Body)).Decode(&s); err != nil {
		return nil, err
	}
	return s.sample(), nil
}

func (s *statsWire) sample() *StatsSample {
	out := &StatsSample{CPUTotal: s.CPUStats.CPUUsage.TotalUsage, System: s.CPUStats.SystemUsage, CPUs: s.CPUStats.OnlineCPUs,
		MemLimit: s.MemoryStats.Limit, Pids: s.PidsStats.Current}
	if t, err := time.Parse(time.RFC3339Nano, s.Read); err == nil && t.Year() > 1 {
		out.Read = t
	} else {
		out.Read = time.Now()
	}
	if out.CPUs == 0 {
		out.CPUs = len(s.CPUStats.CPUUsage.PercpuUsage)
	}
	mem := s.MemoryStats.Usage
	// cgroup v2: inactive_file; cgroup v1: total_inactive_file (docker CLI rule)
	if v, ok := s.MemoryStats.Stats["inactive_file"]; ok && v < mem {
		mem -= v
	} else if v, ok := s.MemoryStats.Stats["total_inactive_file"]; ok && v < mem {
		mem -= v
	}
	out.MemUsage = mem
	for _, n := range s.Networks {
		out.NetRx += n.RxBytes
		out.NetTx += n.TxBytes
	}
	for _, b := range s.BlkioStats.IOServiceBytesRecursive {
		switch b.Op {
		case "read", "Read":
			out.BlkRead += b.Value
		case "write", "Write":
			out.BlkWrite += b.Value
		}
	}
	return out
}

// LogOptions select container log lines.
type LogOptions struct {
	Stdout, Stderr bool
	Since, Until   string // unix seconds (may have a fraction) or ""
	Tail           int    // <= 0 = all
	Follow         bool
}

// LogsStream opens GET /containers/{id}/logs (timestamps on). tty = the container has a TTY (the body is raw, not
// multiplexed). The caller closes the reader.
func (c *Client) LogsStream(ctx context.Context, id string, o LogOptions, tty bool) (*LogReader, error) {
	q := url.Values{"timestamps": {"1"}}
	if o.Stdout {
		q.Set("stdout", "1")
	}
	if o.Stderr {
		q.Set("stderr", "1")
	}
	if o.Tail > 0 {
		q.Set("tail", strconv.Itoa(o.Tail))
	} else {
		q.Set("tail", "all")
	}
	if o.Since != "" {
		q.Set("since", o.Since)
	}
	if o.Until != "" {
		q.Set("until", o.Until)
	}
	if o.Follow {
		q.Set("follow", "1")
	}
	resp, err := c.do(ctx, "GET", "/containers/"+id+"/logs", q, nil, "")
	if err != nil {
		return nil, err
	}
	return &LogReader{rc: resp.Body, tty: tty, br: bufio.NewReaderSize(resp.Body, 64*1024)}, nil
}

// LogReader yields demultiplexed log lines from a logs stream.
type LogReader struct {
	rc      io.ReadCloser
	br      *bufio.Reader
	tty     bool
	pending []LogLine
	partial map[string][]byte
}

// Close stops the stream.
func (l *LogReader) Close() error { return l.rc.Close() }

// Next returns the next complete line (io.EOF at the end of a non-follow stream).
func (l *LogReader) Next() (LogLine, error) {
	for len(l.pending) == 0 {
		if l.tty {
			b, err := l.br.ReadBytes('\n')
			if len(b) > 0 {
				l.pending = append(l.pending, splitTs("stdout", bytes.TrimRight(b, "\r\n")))
			}
			if err != nil && len(l.pending) == 0 {
				return LogLine{}, err
			}
			continue
		}
		var hdr [8]byte
		if _, err := io.ReadFull(l.br, hdr[:]); err != nil {
			if rest := l.flushPartial(); len(rest) > 0 {
				l.pending = rest
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				err = io.EOF
			}
			return LogLine{}, err
		}
		st := "stdout"
		if hdr[0] == 2 {
			st = "stderr"
		}
		n := int(binary.BigEndian.Uint32(hdr[4:8]))
		if n > 16<<20 {
			return LogLine{}, fmt.Errorf("log frame too large (%d bytes)", n)
		}
		buf := make([]byte, n)
		if _, err := io.ReadFull(l.br, buf); err != nil {
			return LogLine{}, err
		}
		if l.partial == nil {
			l.partial = map[string][]byte{}
		}
		data := append(l.partial[st], buf...)
		for {
			i := bytes.IndexByte(data, '\n')
			if i < 0 {
				break
			}
			l.pending = append(l.pending, splitTs(st, bytes.TrimRight(data[:i], "\r")))
			data = data[i+1:]
		}
		l.partial[st] = append([]byte(nil), data...)
	}
	ln := l.pending[0]
	l.pending = l.pending[1:]
	return ln, nil
}

func (l *LogReader) flushPartial() []LogLine {
	var out []LogLine
	for st, b := range l.partial {
		if len(b) > 0 {
			out = append(out, splitTs(st, b))
		}
	}
	l.partial = nil
	return out
}

func splitTs(stream string, b []byte) LogLine {
	s := string(b)
	if i := bytes.IndexByte(b, ' '); i >= 20 && i < 40 && b[4] == '-' {
		return LogLine{Stream: stream, Ts: s[:i], Text: s[i+1:]}
	}
	return LogLine{Stream: stream, Text: s}
}

// ExecConfig is POST /containers/{id}/exec.
type ExecConfig struct {
	Cmd          []string `json:"Cmd"`
	Tty          bool     `json:"Tty"`
	AttachStdin  bool     `json:"AttachStdin"`
	AttachStdout bool     `json:"AttachStdout"`
	AttachStderr bool     `json:"AttachStderr"`
	Env          []string `json:"Env,omitempty"`
	User         string   `json:"User,omitempty"`
	WorkingDir   string   `json:"WorkingDir,omitempty"`
}

// ExecCreate creates an exec instance and returns its id.
func (c *Client) ExecCreate(ctx context.Context, id string, cfg ExecConfig) (string, error) {
	var out struct {
		ID string `json:"Id"`
	}
	err := c.jsonDo(ctx, "POST", "/containers/"+id+"/exec", nil, cfg, &out)
	return out.ID, err
}

// ExecAttach starts an exec and returns the hijacked connection (raw TTY bytes when tty, else multiplexed).
func (c *Client) ExecAttach(ctx context.Context, execID string, tty bool) (io.ReadWriteCloser, error) {
	api, err := c.Negotiate(ctx)
	if err != nil {
		return nil, err
	}
	body, _ := json.Marshal(map[string]bool{"Detach": false, "Tty": tty})
	req, err := http.NewRequestWithContext(ctx, "POST", c.url(api, "/exec/"+execID+"/start", nil), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		resp.Body.Close()
		if resp.StatusCode == 404 {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, msgOf(b))
		}
		if resp.StatusCode >= 300 {
			return nil, &APIError{Status: resp.StatusCode, Message: msgOf(b)}
		}
		return nil, fmt.Errorf("docker exec start: expected 101 Switching Protocols, got %d", resp.StatusCode)
	}
	rwc, ok := resp.Body.(io.ReadWriteCloser)
	if !ok {
		resp.Body.Close()
		return nil, errors.New("docker exec start: the connection cannot be written")
	}
	return rwc, nil
}

// ExecResize resizes the TTY of an exec.
func (c *Client) ExecResize(ctx context.Context, execID string, rows, cols int) error {
	q := url.Values{"h": {strconv.Itoa(rows)}, "w": {strconv.Itoa(cols)}}
	return c.jsonDo(ctx, "POST", "/exec/"+execID+"/resize", q, nil, nil)
}

// ExecState is GET /exec/{id}/json (subset).
type ExecState struct {
	Running  bool `json:"Running"`
	ExitCode *int `json:"ExitCode"`
	Pid      int  `json:"Pid"`
}

// ExecInspect reads an exec's state.
func (c *Client) ExecInspect(ctx context.Context, execID string) (*ExecState, error) {
	var out ExecState
	if err := c.jsonDo(ctx, "GET", "/exec/"+execID+"/json", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ImageRow is an item of GET /images/json.
type ImageRow struct {
	ID          string            `json:"Id"`
	RepoTags    []string          `json:"RepoTags"`
	RepoDigests []string          `json:"RepoDigests"`
	Created     int64             `json:"Created"`
	Size        int64             `json:"Size"`
	Containers  int64             `json:"Containers"`
	Labels      map[string]string `json:"Labels"`
}

// Images lists the images (top-level).
func (c *Client) Images(ctx context.Context) ([]ImageRow, error) {
	var out []ImageRow
	return out, c.jsonDo(ctx, "GET", "/images/json", nil, nil, &out)
}

// ImageDelete removes an image by id without force (a used image fails with 409); returns the deleted / untagged refs.
func (c *Client) ImageDelete(ctx context.Context, id string) error {
	return c.jsonDo(ctx, "DELETE", "/images/"+id, nil, nil, nil)
}

// DiskUsage is GET /system/df (subset).
type DiskUsage struct {
	LayersSize int64 `json:"LayersSize"`
	Images     []struct {
		ID         string `json:"Id"`
		Size       int64  `json:"Size"`
		SharedSize int64  `json:"SharedSize"`
		Containers int64  `json:"Containers"`
	} `json:"Images"`
	Containers []struct {
		ID         string `json:"Id"`
		SizeRw     int64  `json:"SizeRw"`
		SizeRootFs int64  `json:"SizeRootFs"`
		State      string `json:"State"`
	} `json:"Containers"`
	Volumes []struct {
		Name      string `json:"Name"`
		UsageData *struct {
			Size     int64 `json:"Size"`
			RefCount int64 `json:"RefCount"`
		} `json:"UsageData"`
	} `json:"Volumes"`
	BuildCache []struct {
		Size   int64 `json:"Size"`
		InUse  bool  `json:"InUse"`
		Shared bool  `json:"Shared"`
	} `json:"BuildCache"`
}

// SystemDF reads docker system df.
func (c *Client) SystemDF(ctx context.Context) (*DiskUsage, error) {
	var out DiskUsage
	if err := c.jsonDo(ctx, "GET", "/system/df", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetRaw returns a versioned GET as raw JSON (support bundle: /info, /containers/json).
func (c *Client) GetRaw(ctx context.Context, path string, q url.Values) (json.RawMessage, error) {
	resp, err := c.do(ctx, "GET", path, q, nil, "")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("docker GET %s: HTTP %d", path, resp.StatusCode)
	}
	return json.RawMessage(b), nil
}
