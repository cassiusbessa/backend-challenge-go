package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// executor stands in for the processes: it answers the listing and records
// every command it was asked to run.
type executor struct {
	mu     sync.Mutex
	asked  [][]string
	listed string
	fail   string
}

func (e *executor) run(_ context.Context, args []string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.asked = append(e.asked, args)
	if e.fail != "" && slices.Contains(args, e.fail) {
		return "", errors.New("exit status 1: refused")
	}
	if slices.Contains(args, "ps") || slices.Contains(args, "get") {
		return e.listed, nil
	}
	return "", nil
}

// Each mode of each target kills the first replica the target lists, by the
// command the target offers.
func TestKillHalfway_runsTheCommandsOfTheTargetAndTheMode(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		target, mode, listed string
		want                 [][]string
	}{
		{target: "compose", mode: killGraceful, listed: "c0ffee\nbeef\n", want: [][]string{
			{"docker", "compose", "-f", "/repo/compose.yaml", "ps", "-q", "wager"},
			{"docker", "stop", "c0ffee"},
		}},
		{target: "compose", mode: killForced, listed: "c0ffee\n", want: [][]string{
			{"docker", "compose", "-f", "/repo/compose.yaml", "ps", "-q", "wager"},
			{"docker", "kill", "c0ffee"},
		}},
		{target: "cluster", mode: killGraceful, listed: "wager-7d9-abc", want: [][]string{
			{"kubectl", "--context", "kind-junglegaming", "--namespace", "junglegaming", "get", "pods", "-l", "app=wager", "-o", "jsonpath={.items[0].metadata.name}"},
			{"kubectl", "--context", "kind-junglegaming", "--namespace", "junglegaming", "delete", "pod", "wager-7d9-abc"},
		}},
		{target: "cluster", mode: killForced, listed: "wager-7d9-abc", want: [][]string{
			{"kubectl", "--context", "kind-junglegaming", "--namespace", "junglegaming", "get", "pods", "-l", "app=wager", "-o", "jsonpath={.items[0].metadata.name}"},
			{"kubectl", "--context", "kind-junglegaming", "--namespace", "junglegaming", "delete", "pod", "wager-7d9-abc", "--grace-period=0", "--force"},
		}},
	} {
		t.Run(tc.target+" "+tc.mode, func(t *testing.T) {
			t.Parallel()
			fake := &executor{listed: tc.listed}
			o := options{target: tc.target, kill: tc.mode, root: "/repo", kubectl: "kubectl", context: "kind-junglegaming", namespace: "junglegaming", duration: time.Minute, commands: fake.run}
			record, err := newLoad(o).killHalfway(context.Background(), longAgo)
			if err != nil {
				t.Fatalf("killHalfway = %v, want nil", err)
			}
			if !slices.EqualFunc(fake.asked, tc.want, slices.Equal) {
				t.Fatalf("commands = %q, want %q", fake.asked, tc.want)
			}
			if record.Replica != strings.Fields(tc.listed)[0] || record.Mode != tc.mode || record.AfterSeconds < 30 {
				t.Fatalf("record = %+v, want the first replica, the mode and no earlier than the middle of the window", record)
			}
		})
	}
}

// A command that fails fails the run, naming the command.
func TestKillHalfway_namesTheCommandThatFailed(t *testing.T) {
	t.Parallel()
	for name, fake := range map[string]*executor{
		"the listing": {fail: "ps"},
		"the stop":    {listed: "c0ffee", fail: "stop"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			o := options{target: "compose", kill: killGraceful, root: "/repo", duration: time.Millisecond, commands: fake.run}
			_, err := newLoad(o).killHalfway(context.Background(), longAgo)
			if err == nil || !strings.Contains(err.Error(), "kill a replica: docker ") || !strings.Contains(err.Error(), "refused") {
				t.Fatalf("killHalfway when %s fails = %v, want the command named", name, err)
			}
		})
	}
}

// A target with no replica to list is a failure, not a kill of nothing.
func TestKillHalfway_failsWhenNoReplicaIsListed(t *testing.T) {
	t.Parallel()
	o := options{target: "compose", kill: killForced, root: "/repo", duration: time.Millisecond, commands: (&executor{}).run}
	if _, err := newLoad(o).killHalfway(context.Background(), longAgo); err == nil || !strings.Contains(err.Error(), "answered no replica to kill") {
		t.Fatalf("killHalfway with nothing listed = %v, want the failure", err)
	}
}

// A run that kills a replica halfway records it in the report, and a kill that
// fails fails the run.
func TestRun_recordsTheKillAndFailsWhenItFails(t *testing.T) {
	t.Parallel()
	_, server := newFakeService(t)
	o := optionsFor(t, server)
	o.kill = killForced
	o.commands = (&executor{listed: "c0ffee"}).run
	var out strings.Builder
	if err := run(context.Background(), o, &out); err != nil {
		t.Fatalf("run with a kill = %v, want nil", err)
	}
	if !strings.Contains(out.String(), "killed c0ffee (forced)") {
		t.Fatalf("output = %q, want the kill in it", out.String())
	}
	o.commands = (&executor{listed: "c0ffee", fail: "kill"}).run
	if err := run(context.Background(), o, &out); err == nil || !strings.Contains(err.Error(), "kill a replica: docker kill c0ffee") {
		t.Fatalf("run whose kill fails = %v, want the command named", err)
	}
}

// The real process answers its output, and its failure carries what it wrote
// on the error stream.
func TestRunCommand_answersTheOutputOrTheErrorStream(t *testing.T) {
	t.Parallel()
	if out, err := runCommand(context.Background(), []string{"sh", "-c", "echo replica"}); err != nil || out != "replica\n" {
		t.Fatalf("runCommand of an echo = %q, %v, want its output", out, err)
	}
	if _, err := runCommand(context.Background(), []string{"sh", "-c", "echo gone >&2; exit 3"}); err == nil || !strings.Contains(err.Error(), "gone") {
		t.Fatalf("runCommand of a failure = %v, want the error stream in it", err)
	}
}

// A signal before the middle of the window leaves every replica alone.
func TestKillHalfway_killsNothingAfterTheSignal(t *testing.T) {
	t.Parallel()
	fake := &executor{listed: "c0ffee"}
	signalled, cancel := context.WithCancel(context.Background())
	cancel()
	o := options{target: "compose", kill: killForced, root: "/repo", duration: time.Hour, commands: fake.run}
	if _, err := newLoad(o).killHalfway(signalled, farAhead); !errors.Is(err, context.Canceled) || len(fake.asked) != 0 {
		t.Fatalf("killHalfway after the signal = %v with %d commands, want the cancellation and none", err, len(fake.asked))
	}
}
