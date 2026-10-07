package server

import (
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
func (p *postProcessor) apply(ctx context.Context, r *clusterExecResult) {
	if r.Error != "" || r.ExitCode != 0 || r.Stdout == "" {
		return
	}

	if p.query != nil {
		filtered, err := runJQ(ctx, p.query, r.Stdout)
		if err != nil {
			r.Stdout = ""
			r.Error = err.Error()
			return
		}
		r.Stdout = filtered
	}

	if p.save && r.Stdout != "" {
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

// runJQ applies code to every JSON document in stdout. String results are
// printed raw (like jq -r), everything else as indented JSON.
func runJQ(ctx context.Context, code *gojq.Code, stdout string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, jqTimeout)
	defer cancel()
	var out strings.Builder
	dec := json.NewDecoder(strings.NewReader(stdout))
	for {
		var doc any
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", fmt.Errorf("jq: kubectl output is not JSON (pass -o json): %w", err)
		}

		iter := code.RunWithContext(ctx, doc)
		for {
			v, ok := iter.Next()
			if !ok {
				break
			}
			if err, isErr := v.(error); isErr {
				if errors.Is(err, context.DeadlineExceeded) {
					return "", fmt.Errorf("jq: evaluation exceeded %s deadline; narrow the expression", jqTimeout)
				}
				return "", fmt.Errorf("jq: %w", err)
			}
			if err := appendJQResult(&out, v); err != nil {
				return "", err
			}
		}
	}
	return out.String(), nil
}

// appendJQResult renders one jq result onto out, one per line: strings raw,
// everything else as indented JSON. It refuses a result that would take the
// output past maxJQOutputBytes before writing it, so a single huge value
// cannot get into the buffer first and only then trip the cap.
func appendJQResult(out *strings.Builder, v any) error {
	var rendered string
	if s, isStr := v.(string); isStr {
		rendered = s
	} else {
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Errorf("jq: rendering result: %w", err)
		}
		rendered = string(b)
	}
	if out.Len()+len(rendered)+1 > maxJQOutputBytes {
		return fmt.Errorf("jq: output exceeds %d bytes; narrow the expression", maxJQOutputBytes)
	}
	out.WriteString(rendered)
	out.WriteByte('\n')
	return nil
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
