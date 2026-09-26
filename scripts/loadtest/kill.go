package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// killHalfway stops or kills one replica at the middle of the window, by the
// means the target offers, and answers what it did. The load does not stop for
// it: the arrivals in flight on that replica are what the resolution settles.
func (l *load) killHalfway(ctx context.Context, start time.Time) (*killRecord, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Until(start.Add(l.opts.duration / 2))):
	}
	listed, err := l.exec(ctx, l.opts.findReplica())
	if err != nil {
		return nil, err
	}
	replica, _, _ := strings.Cut(strings.TrimSpace(listed), "\n")
	if replica == "" {
		return nil, fmt.Errorf("%s answered no replica to kill", strings.Join(l.opts.findReplica(), " "))
	}
	at := time.Now()
	if _, err := l.exec(ctx, l.opts.stopReplica(replica)); err != nil {
		return nil, err
	}
	return &killRecord{Replica: replica, Mode: l.opts.kill, At: at.UTC().Format(time.RFC3339), AfterSeconds: at.Sub(start).Seconds()}, nil
}

// findReplica is the command that lists the replicas, the first one first: a
// container of the Compose service, or a pod of the Deployment.
func (o options) findReplica() []string {
	if o.target == "cluster" {
		return append(o.kubectlArgs(), "get", "pods", "-l", "app=wager", "-o", "jsonpath={.items[0].metadata.name}")
	}
	return []string{"docker", "compose", "-f", filepath.Join(o.root, "compose.yaml"), "ps", "-q", "wager"}
}

// stopReplica is the command that ends the replica. The graceful stop of a
// container waits the stop_grace_period the Compose gave it before the kill; a
// pod leaves the Service during its preStop, and the forced deletion skips both.
func (o options) stopReplica(replica string) []string {
	switch {
	case o.target == "cluster" && o.kill == killForced:
		return append(o.kubectlArgs(), "delete", "pod", replica, "--grace-period=0", "--force")
	case o.target == "cluster":
		return append(o.kubectlArgs(), "delete", "pod", replica)
	case o.kill == killForced:
		return []string{"docker", "kill", replica}
	}
	return []string{"docker", "stop", replica}
}

func (o options) kubectlArgs() []string {
	return []string{o.kubectl, "--context", o.context, "--namespace", o.namespace}
}

// exec runs one command and answers its output, and a failure names the
// command.
func (l *load) exec(ctx context.Context, args []string) (string, error) {
	run := runCommand
	if l.opts.commands != nil {
		run = l.opts.commands
	}
	out, err := run(ctx, args)
	if err != nil {
		return "", fmt.Errorf("kill a replica: %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

func runCommand(ctx context.Context, args []string) (string, error) {
	command := exec.CommandContext(ctx, args[0], args[1:]...) //nolint:gosec // the operator names the binaries, and the arguments are fixed here
	var stderr bytes.Buffer
	command.Stderr = &stderr
	out, err := command.Output()
	if err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}
