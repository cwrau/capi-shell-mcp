package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/exec"
	"github.com/itchyny/gojq"
)

const (
	// maxJQOutputBytes bounds a single cluster's jq output so a runaway
	// expression can't exhaust the daemon's memory.
	maxJQOutputBytes = 10 * 1024 * 1024

	savedOutputPrefix = "out-"
	savedOutputMaxAge = time.Hour
)

// jqTimeout bounds one cluster's jq evaluation. The output cap does not: an
// expression that emits nothing and never ends, or builds a huge value
// before returning, would otherwise hold a CPU and memory of the shared
// daemon until the request ends. A variable so tests can shorten it.
var jqTimeout = 30 * time.Second

// outputDir is the private directory saved outputs go in: the daemon
// chooses it, so a caller can never direct a write to a path of its own.
func (s *ToolServer) outputDir() string {
	if s.outputDirOverride != "" {
		return s.outputDirOverride
	}
	base := os.Getenv("XDG_RUNTIME_DIR")
	if base == "" {
		base = os.TempDir()
	}
	return filepath.Join(base, "capi-shell-mcp")
}

// postProcessor applies the optional jq filter and save-to-file step to a
// successful cluster result. A nil *postProcessor is not valid; use
// newPostProcessor.
type postProcessor struct {
	query *gojq.Code // nil when no filter was requested
	save  bool
	dir   string
}

// newPostProcessor compiles jq once, up front, so a bad expression fails
// the whole call before any cluster is touched. gojq runs in-process and,
// compiled without an environment loader, sees an empty environment.
func newPostProcessor(jq string, save bool, dir string) (*postProcessor, error) {
	p := &postProcessor{save: save, dir: dir}
	if jq == "" {
		return p, nil
	}
	query, err := gojq.Parse(jq)
	if err != nil {
		return nil, fmt.Errorf("jq: invalid expression: %w", err)
	}
	code, err := gojq.Compile(query)
	if err != nil {
		return nil, fmt.Errorf("jq: invalid expression: %w", err)
	}
	p.query = code
	return p, nil
}

// maxInputBytes is how much kubectl output one cluster may produce. Output
// that is returned as it is stays at the response sized cap. Output that a
// jq filter shrinks, or that is written to a file, may be larger, so a big
// cluster's list can still be reduced to what is wanted.
func (p *postProcessor) maxInputBytes() int {
	if p.query != nil || p.save {
		return exec.MaxProcessedOutputBytes
	}
	return exec.DefaultMaxOutputBytes
}

// apply filters and/or saves r.Stdout. It only touches successful results
// with output; failures keep their raw stdout/stderr for diagnosis.
//
// With a jq filter the raw output is replaced by the filter's output as a
// JSON value in r.Result, or, with save_output, by a file holding that value
// (r.ResultFile). Without a filter the raw output stays in r.Stdout, or in
// r.StdoutFile when it is saved.
func (p *postProcessor) apply(ctx context.Context, r *clusterExecResult) {
	if r.Error != "" || r.ExitCode != 0 || r.Stdout == "" {
		return
	}

	if p.query != nil {
		result, err := runJQ(ctx, p.query, r.Stdout)
		r.Stdout = ""
		if err != nil {
			r.Error = err.Error()
			return
		}
		if len(result) == 0 {
			return // the filter produced nothing: nothing to report
		}
		if !p.save {
			// The response is indented as a whole, so check what that makes
			// of this value before it goes in.
			if _, err := indentJSON(result, inlineResultDepth, maxJQOutputBytes); err != nil {
				r.Error = err.Error()
				return
			}
			r.Result = result
			return
		}
		indented, err := indentJSON(result, 0, maxJQOutputBytes)
		if err != nil {
			r.Error = err.Error()
			return
		}
		indented = append(indented, '\n')
		path, err := saveOutput(p.dir, string(indented))
		if err != nil {
			r.Error = fmt.Sprintf("saving result: %v", err)
			return
		}
		r.ResultFile = path
		r.ResultBytes = len(indented)
		return
	}

	if p.save {
		path, err := saveOutput(p.dir, r.Stdout)
		if err != nil {
			r.Error = fmt.Sprintf("saving output: %v", err)
			return
		}
		r.StdoutBytes = len(r.Stdout)
		r.Stdout = ""
		r.StdoutFile = path
	}
}

// runJQ applies code to every JSON document in stdout and returns what it
// produced as one JSON value (see jqCollector.result), nil when it produced
// nothing.
func runJQ(ctx context.Context, code *gojq.Code, stdout string) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, jqTimeout)
	defer cancel()
	var out jqCollector
	dec := json.NewDecoder(strings.NewReader(stdout))
	for {
		var doc any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("jq: kubectl output is not JSON (pass -o json): %w", err)
		}

		iter := code.RunWithContext(ctx, doc)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, isErr := v.(error); isErr {
				if errors.Is(err, context.DeadlineExceeded) {
					return nil, fmt.Errorf("jq: evaluation exceeded %s deadline; narrow the expression", jqTimeout)
				}
				return nil, fmt.Errorf("jq: %w", err)
			}
			if err := out.add(v); err != nil {
				return nil, err
			}
		}
	}
	return out.result(), nil
}

// jqCollector gathers one cluster's jq outputs as JSON, within
// maxJQOutputBytes. It refuses an output that would take it past the cap
// before keeping it, so a single huge value cannot get into the buffer first
// and only then trip the cap.
type jqCollector struct {
	raws []json.RawMessage
	size int
}

func (c *jqCollector) add(v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false) // keep <, > and & readable in annotations and the like
	if err := enc.Encode(v); err != nil {
		return fmt.Errorf("jq: rendering result: %w", err)
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	if c.size+len(raw)+1 > maxJQOutputBytes {
		return fmt.Errorf("jq: output exceeds %d bytes; narrow the expression", maxJQOutputBytes)
	}
	c.raws = append(c.raws, append(json.RawMessage(nil), raw...))
	c.size += len(raw) + 1
	return nil
}

// result is nothing when the filter produced no output, the value itself
// when it produced exactly one, and an array of the values otherwise. A
// filter that always wants an array can say so: [ ... ].
func (c *jqCollector) result() json.RawMessage {
	switch len(c.raws) {
	case 0:
		return nil
	case 1:
		return c.raws[0]
	}
	return json.RawMessage("[" + string(bytes.Join(rawSlices(c.raws), []byte(","))) + "]")
}

// inlineResultDepth is how deep an inline result sits in the response, which
// is indented as an array of cluster objects: its lines start two levels in.
const inlineResultDepth = 2

// indentJSON pretty-prints src, which must be valid compact JSON as
// jqCollector produces it, two spaces per level, starting baseDepth levels
// in. It gives up with an error as soon as the output would pass limit bytes.
//
// The limit matters: nesting multiplies the size of an indented value, so a
// small compact value can indent to many times the compact cap.
// encoding/json's Indent builds all of that before a caller can look at its
// size, so it cannot be used on a value that has not been bounded.
func indentJSON(src []byte, baseDepth, limit int) ([]byte, error) {
	out := make([]byte, 0, min(2*len(src), limit))
	depth := baseDepth
	inString, escaped := false, false
	tooBig := func() error {
		return fmt.Errorf("jq: output exceeds %d bytes once indented; narrow the expression or flatten its nesting", limit)
	}
	newline := func() error {
		if len(out)+1+2*depth > limit {
			return tooBig()
		}
		out = append(out, '\n')
		for range depth {
			out = append(out, ' ', ' ')
		}
		return nil
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
		} else {
			switch c {
			case '"':
				inString = true
				out = append(out, c)
			case '{', '[':
				closer := byte('}')
				if c == '[' {
					closer = ']'
				}
				out = append(out, c)
				if i+1 < len(src) && src[i+1] == closer {
					out = append(out, closer) // an empty container stays on one line
					i++
					break
				}
				depth++
				if err := newline(); err != nil {
					return nil, err
				}
			case '}', ']':
				depth--
				if err := newline(); err != nil {
					return nil, err
				}
				out = append(out, c)
			case ',':
				out = append(out, c)
				if err := newline(); err != nil {
					return nil, err
				}
			case ':':
				out = append(out, ':', ' ')
			default:
				out = append(out, c)
			}
		}
		if len(out) > limit {
			return nil, tooBig()
		}
	}
	return out, nil
}

func rawSlices(raws []json.RawMessage) [][]byte {
	out := make([][]byte, len(raws))
	for i, r := range raws {
		out[i] = r
	}
	return out
}

// ensurePrivateDir creates dir (0700) if needed and makes sure it is a real
// directory owned by this process and closed to everyone else. The
// fallback location is under the shared temp directory, where somebody else
// could have created the name first, as a symlink or as a directory they own.
func ensurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("server: %s is not a plain directory (a symlink or a file), refusing to save output there", dir)
	}
	if st, ok := info.Sys().(*syscall.Stat_t); ok && int(st.Uid) != os.Geteuid() {
		return fmt.Errorf("server: %s is owned by uid %d, not by this process, refusing to save output there", dir, st.Uid)
	}
	if info.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return fmt.Errorf("server: closing %s to other users: %w", dir, err)
		}
	}
	return nil
}

// saveOutput writes content to a new 0600 file in dir (see ensurePrivateDir)
// and returns its path, first removing saved outputs older than an hour.
func saveOutput(dir, content string) (string, error) {
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	sweepSavedOutputs(dir, savedOutputMaxAge)

	f, err := os.CreateTemp(dir, savedOutputPrefix+"*.txt") // CreateTemp creates with mode 0600
	if err != nil {
		return "", err
	}
	if _, err := f.WriteString(content); err != nil {
		f.Close()
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Close(); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func sweepSavedOutputs(dir string, maxAge time.Duration) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-maxAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), savedOutputPrefix) {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Before(cutoff) {
			os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
