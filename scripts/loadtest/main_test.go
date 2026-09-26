package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// With no flag, the run loads the Compose replicas through the balancer and
// reads them by instance, with the figures of the design.
func TestReadFlags_answersTheDefaultsOfTheDesign(t *testing.T) {
	t.Parallel()
	got, err := readFlags(nil, io.Discard)
	if err != nil {
		t.Fatalf("readFlags with no flag = %v, want nil", err)
	}
	want := options{
		target: "compose", address: "http://localhost:8090", replicaLabel: "instance", root: ".",
		idp: "http://localhost:8080", realm: "junglegaming", prometheus: "http://localhost:9095",
		internalClient: "wallet-internal", internalSecret: "wallet-internal-local",
		providerClient: "provider-a", providerSecret: "provider-a-local",
		replicas: 3, duration: 60 * time.Second, concurrency: 32, wallets: 100, seed: 1,
		kubectl: "kubectl", context: "kind-junglegaming", namespace: "junglegaming",
		report: filepath.Join(".", ".quality/load/report.json"),
		settle: 15 * time.Second, drainWait: time.Minute, resolveWait: time.Minute,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("defaults = %+v, want %+v", got, want)
	}
}

// Each target decides the address and the label the replicas are read by.
func TestWithTarget_choosesTheAddressAndTheLabelOfTheTarget(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]target{
		"compose": {address: "http://localhost:8090", replica: "instance", selector: `job="wager",pod=""`},
		"cluster": {address: "http://localhost:8091", replica: "pod", selector: `job="wager",pod!=""`},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got, err := readFlags([]string{"-target", name, "-root", "/repo"}, io.Discard)
			if err != nil {
				t.Fatalf("readFlags of the %s target = %v, want nil", name, err)
			}
			if got.address != want.address || got.replicaLabel != want.replica || newBackend(got).selector != want.selector {
				t.Fatalf("%s target = %s by %s over %s, want %s by %s over %s", name, got.address, got.replicaLabel, newBackend(got).selector, want.address, want.replica, want.selector)
			}
			if got.report != "/repo/.quality/load/report.json" {
				t.Fatalf("report of the %s target = %s, want it under the root", name, got.report)
			}
		})
	}
}

// A value no run can use is refused before anything is sent, naming the flag.
func TestReadFlags_refusesWhatNoRunCanUse(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		args []string
		flag string
	}{
		{args: []string{"-target", "swarm"}, flag: "-target"},
		{args: []string{"-duration", "0s"}, flag: "-duration"},
		{args: []string{"-duration", "-1s"}, flag: "-duration"},
		{args: []string{"-concurrency", "0"}, flag: "-concurrency"},
		{args: []string{"-replicas", "0"}, flag: "-replicas"},
		{args: []string{"-replicas", "-2"}, flag: "-replicas"},
		{args: []string{"-wallets", "0"}, flag: "-wallets"},
		{args: []string{"-kill", "sideways"}, flag: "-kill"},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			t.Parallel()
			_, err := readFlags(tc.args, io.Discard)
			if err == nil || !strings.Contains(err.Error(), tc.flag) {
				t.Fatalf("readFlags(%v) = %v, want a refusal naming %s", tc.args, err, tc.flag)
			}
		})
	}
}

// The absolute report path is kept as given; a relative one lands under root.
func TestWithTarget_keepsAnAbsoluteReportPath(t *testing.T) {
	t.Parallel()
	got, err := readFlags([]string{"-report", "/tmp/elsewhere.json"}, io.Discard)
	if err != nil {
		t.Fatalf("readFlags with an absolute report = %v, want nil", err)
	}
	if got.report != "/tmp/elsewhere.json" {
		t.Fatalf("report = %s, want the absolute path kept", got.report)
	}
}

// The exit code is two for flags it cannot use, one for a verdict that failed,
// and zero otherwise; the run receives the options the flags read.
func TestCommand_answersTheExitCodeOfTheVerdict(t *testing.T) {
	t.Parallel()
	var handed options
	passing := func(_ context.Context, o options, _ io.Writer) error { handed = o; return nil }
	if code := command([]string{"-target", "cluster", "-replicas", "5"}, io.Discard, io.Discard, passing); code != 0 {
		t.Fatalf("exit code of a passing run = %d, want 0", code)
	}
	if handed.target != "cluster" || handed.replicas != 5 {
		t.Fatalf("options handed to the run = %s with %d replicas, want cluster with 5", handed.target, handed.replicas)
	}
	var stderr bytes.Buffer
	failing := func(context.Context, options, io.Writer) error { return errors.New("verdict failed: wallet w") }
	if code := command(nil, io.Discard, &stderr, failing); code != 1 || !strings.Contains(stderr.String(), "wallet w") {
		t.Fatalf("failing run = exit %d with %q, want 1 naming the failure", code, stderr.String())
	}
	if code := command([]string{"-replicas", "x"}, io.Discard, io.Discard, passing); code != 2 {
		t.Fatalf("exit code of an unreadable flag = %d, want 2", code)
	}
}
