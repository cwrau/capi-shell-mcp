package server

import (
	"context"
	"fmt"
	"strings"
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

func TestExecReadonlyJQResultThatIndentsPastTheCapIsRefused(t *testing.T) {
	for _, save := range []bool{false, true} {
		s, _ := newFakeToolServer(t, "cluster-1")
		s.outputDirOverride = t.TempDir()
		s.execer.(*fakeExecer).scriptedStdout = podListJSON

		// 3000 levels of nesting: about 18 KB compact, tens of MB once indented.
		results, result := readonlyResults(t, s, ReadonlyExecInput{
			Clusters:   []string{clusterOneTarget},
			Args:       []string{"get", "pods", "-o", "json"},
			JQ:         "reduce range(3000) as $i (1; [., 1, 1])",
			SaveOutput: save,
		})
		if !result.IsError || len(results) != 1 || !strings.Contains(results[0].Error, "indented") {
			t.Errorf("save=%v: results = %+v isError=%v, want an error about the indented size", save, results, result.IsError)
		}
		if len(results) == 1 && (len(results[0].Result) != 0 || results[0].ResultFile != "") {
			t.Errorf("save=%v: a result was kept: %+v", save, results[0])
		}
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
