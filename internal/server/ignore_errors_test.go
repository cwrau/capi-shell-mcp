package server

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	corev1client "k8s.io/client-go/kubernetes/typed/core/v1"
	rbacv1client "k8s.io/client-go/kubernetes/typed/rbac/v1"

	"github.com/cwrau/capi-shell-mcp/internal/capi"
)

func TestFinishExecResultsIgnoreErrorsKeepsFailuresButDoesNotFailTheCall(t *testing.T) {
	failed := clusterExecResult{Cluster: clusterID{Name: "a"}, ExitCode: 1, Stderr: "the server doesn't have a resource type \"widgets\""}
	unreachable := clusterExecResult{Cluster: clusterID{Name: "b"}, Error: "dial tcp: i/o timeout"}
	fine := clusterExecResult{Cluster: clusterID{Name: "c"}, Stdout: "ok"}

	for name, tc := range map[string]struct {
		ignore    bool
		wantError bool
	}{
		"without the flag a failure fails the call": {ignore: false, wantError: true},
		"with the flag the call succeeds":           {ignore: true, wantError: false},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := finishExecResults([]clusterExecResult{failed, unreachable, fine}, tc.ignore)
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError != tc.wantError {
				t.Errorf("isError = %v, want %v", result.IsError, tc.wantError)
			}
			var kept []clusterExecResult
			if err := json.Unmarshal([]byte(asText(result)), &kept); err != nil {
				t.Fatal(err)
			}
			if len(kept) != 3 {
				t.Fatalf("kept %d results, want all 3: the failures stay visible either way\n%s", len(kept), asText(result))
			}
		})
	}
}

func TestExecReadonlyIgnoreErrorsKeepsTheFailureButIsNotAToolError(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	execer := s.execer.(*fakeExecer)
	execer.scriptedExitCode, execer.scriptedStderr = 1, "error: the server doesn't have a resource type \"widgets\""

	results, result := readonlyResults(t, s, ReadonlyExecInput{
		Clusters:     []string{clusterOneTarget},
		Args:         []string{"get", "widgets"},
		IgnoreErrors: true,
	})
	if result.IsError {
		t.Errorf("isError = true, want false with ignore_errors")
	}
	if len(results) != 1 || results[0].ExitCode != 1 || results[0].Stderr == "" {
		t.Errorf("results = %+v, want the failed cluster still listed with its exit code and stderr", results)
	}
}

func TestExecReadonlyIgnoreErrorsCoversAClusterThatCannotBeReached(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")
	s.clientsFromReadonlySource = func(string) (corev1client.CoreV1Interface, rbacv1client.RbacV1Interface, capi.APIResourceLister, error) {
		return nil, nil, nil, errors.New("dial tcp: i/o timeout")
	}

	for _, ignore := range []bool{false, true} {
		results, result := readonlyResults(t, s, ReadonlyExecInput{
			Clusters:     []string{clusterOneTarget},
			Args:         []string{"get", "pods"},
			IgnoreErrors: ignore,
		})
		if result.IsError == ignore {
			t.Errorf("ignore_errors=%v: isError = %v", ignore, result.IsError)
		}
		if len(results) != 1 || results[0].Error == "" {
			t.Errorf("ignore_errors=%v: results = %+v, want the unreachable cluster listed with its error", ignore, results)
		}
	}
}

func TestExecReadonlyIgnoreErrorsDoesNotHideAFailedCall(t *testing.T) {
	s, _ := newFakeToolServer(t, "cluster-1")

	// A rejected argument list is a mistake in the call, not a cluster that
	// lacks something: it stays an error however the flag is set.
	rejected, err := s.ExecInClusterReadonly(context.Background(), ReadonlyExecInput{Args: []string{"delete", "pods", "--all"}, IgnoreErrors: true})
	if err != nil || !rejected.IsError {
		t.Errorf("rejected args with ignore_errors: result = %+v err = %v, want a tool error", rejected, err)
	}
	// So does a bad jq expression.
	badJQ, err := s.ExecInClusterReadonly(context.Background(), ReadonlyExecInput{Args: []string{"get", "pods", "-o", "json"}, JQ: ".[", IgnoreErrors: true})
	if err != nil || !badJQ.IsError {
		t.Errorf("bad jq with ignore_errors: result = %+v err = %v, want a tool error", badJQ, err)
	}
}

func TestExecInClusterIgnoreErrorsKeepsTheFailureButIsNotAToolError(t *testing.T) {
	s, fc := newFakeToolServer(t, "cluster-1")
	seedOpenStackCluster(t, fc, "cluster-1")
	execer := s.execer.(*fakeExecer)
	execer.scriptedExitCode, execer.scriptedStderr = 2, "no such thing"

	for _, ignore := range []bool{false, true} {
		result, err := s.ExecInCluster(context.Background(), ExecInput{
			Clusters:     []string{clusterOneTarget},
			Command:      []string{"kubectl", "get", "widgets"},
			IgnoreErrors: ignore,
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.IsError == ignore {
			t.Errorf("ignore_errors=%v: isError = %v", ignore, result.IsError)
		}
		var results []clusterExecResult
		if err := json.Unmarshal([]byte(asText(result)), &results); err != nil {
			t.Fatal(err)
		}
		if len(results) != 1 || results[0].ExitCode != 2 {
			t.Errorf("ignore_errors=%v: results = %+v", ignore, results)
		}
	}
}
