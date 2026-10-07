package server

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cwrau/capi-shell-mcp/internal/exec"
	"github.com/cwrau/capi-shell-mcp/internal/shell"
)

func TestExecReadonlyRaisesTheInputCapOnlyWhenTheOutputIsFilteredOrSaved(t *testing.T) {
	for name, tc := range map[string]struct {
		in   ReadonlyExecInput
		want int
	}{
		"inline output keeps the response sized cap": {
			in:   ReadonlyExecInput{Args: []string{"get", "pods", "-o", "json"}},
			want: exec.DefaultMaxOutputBytes,
		},
		"a jq filter shrinks the output first": {
			in:   ReadonlyExecInput{Args: []string{"get", "pods", "-o", "json"}, JQ: ".items | length"},
			want: exec.MaxProcessedOutputBytes,
		},
		"save_output writes it to a file": {
			in:   ReadonlyExecInput{Args: []string{"get", "pods", "-o", "json"}, SaveOutput: true},
			want: exec.MaxProcessedOutputBytes,
		},
	} {
		t.Run(name, func(t *testing.T) {
			s, _ := newFakeToolServer(t, "cluster-1")
			execer := s.execer.(*fakeExecer)
			execer.scriptedStdout = podListJSON
			s.outputDirOverride = t.TempDir()

			tc.in.Clusters = []string{clusterOneTarget}
			readonlyResults(t, s, tc.in)

			if execer.lastMaxOutput != tc.want {
				t.Errorf("kubectl ran with an output cap of %d, want %d", execer.lastMaxOutput, tc.want)
			}
		})
	}
}

// peakExecer records how many ExecFile calls run at the same time.
type peakExecer struct {
	inFlight, peak atomic.Int32
}

func (p *peakExecer) ExecFile(_ context.Context, file string, _ []string, _ shell.Options) (shell.Output, error) {
	n := p.inFlight.Add(1)
	for {
		old := p.peak.Load()
		if n <= old || p.peak.CompareAndSwap(old, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	p.inFlight.Add(-1)
	return shell.Output{Stdout: "ok: " + file}, nil
}

func TestExecReadonlyRunsAtMostAFewClustersAtOnce(t *testing.T) {
	names := make([]string, 24)
	for i := range names {
		names[i] = fmt.Sprintf("cluster-%d", i)
	}
	s, _ := newFakeToolServer(t, names...)
	probe := &peakExecer{}
	s.execer = probe

	results, _ := readonlyResults(t, s, ReadonlyExecInput{Args: []string{"get", "nodes"}})
	if len(results) != len(names) {
		t.Errorf("got %d results, want %d", len(results), len(names))
	}

	if peak := int(probe.peak.Load()); peak > maxParallelReadonly {
		t.Errorf("%d kubectl runs at the same time, want at most %d", peak, maxParallelReadonly)
	} else if peak < 2 {
		t.Errorf("peak concurrency %d: the fan-out is no longer parallel", peak)
	}
}
