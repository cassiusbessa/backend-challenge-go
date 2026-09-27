// Command loadtest loads the replicas of the settlement over HTTP for a fixed
// time and answers whether every wallet it touched closes: balance, version and
// entries against what it counted itself, and the reconciliation of each. There
// is no throughput goal; the exit code is the verdict.
//
// It is a module of its own because the Dockerfile copies cmd/ and internal/
// whole, and the image would be judged stale by every change here. It depends on
// the standard library alone: what it does is HTTP, JSON, os/exec and the
// arithmetic of a percentile.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

// target is one way the replicas run: the address they answer on, the label
// that tells them apart in the metric backend, and the selector of their
// series there.
type target struct {
	address  string
	replica  string
	selector string
}

// The two targets. The Compose replicas sit behind the balancer and are
// discovered by DNS, which labels each by instance; the cluster replicas sit
// behind the NodePort and reach the backend through the agent, which labels
// each by pod. The series of the agent carry an instance too, so the Compose
// ones are the series of the job without a pod.
var targets = map[string]target{
	"compose": {address: "http://localhost:8090", replica: "instance", selector: `job="wager",pod=""`},
	"cluster": {address: "http://localhost:8091", replica: "pod", selector: `job="wager",pod!=""`},
}

// The two ways of killing a replica halfway, and no kill at all.
const (
	killNone     = ""
	killGraceful = "graceful"
	killForced   = "forced"
)

type options struct {
	target         string
	address        string
	replicaLabel   string
	root           string
	idp            string
	realm          string
	internalClient string
	internalSecret string
	providerClient string
	providerSecret string
	prometheus     string
	replicas       int
	duration       time.Duration
	concurrency    int
	wallets        int
	seed           uint64
	kill           string
	kubectl        string
	context        string
	namespace      string
	report         string

	// settle is how long the backend is given to cover the end of the window:
	// two scrapes and the send of the agent. drainWait bounds the wait for the
	// outbox, and resolveWait the resolution of the arrivals left undecided.
	//
	// The outbox publishes one event of a wallet at a time, and the hot wallets
	// of the mix receive more events a second than that delivers: their backlog
	// drains after the window, which is what drainWait has to cover.
	settle      time.Duration
	drainWait   time.Duration
	resolveWait time.Duration

	// commands stands in for the processes the kill runs, and clock for the
	// clock the workers read the window by. Both are nil outside the tests,
	// which is the real process and the real clock.
	commands func(ctx context.Context, args []string) (string, error)
	clock    func() time.Time
}

func main() {
	os.Exit(command(os.Args[1:], os.Stdout, os.Stderr, run))
}

// command reads the flags and hands them to the run, and answers the exit code:
// two for flags it cannot use, one for a verdict that failed.
func command(args []string, stdout, stderr io.Writer, run func(context.Context, options, io.Writer) error) int {
	opts, err := readFlags(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, opts, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

// readFlags reads the arguments into the options and refuses the values no run
// can use, naming the flag.
func readFlags(args []string, stderr io.Writer) (options, error) {
	opts := options{settle: 15 * time.Second, resolveWait: time.Minute}
	set := flag.NewFlagSet("loadtest", flag.ContinueOnError)
	set.SetOutput(stderr)
	set.StringVar(&opts.target, "target", "compose", "replicas to load: compose, behind the balancer, or cluster, behind the NodePort")
	set.StringVar(&opts.root, "root", ".", "repository root, where the Compose file is and the report is written under")
	set.StringVar(&opts.idp, "idp", "http://localhost:8080", "base address of the identity provider")
	set.StringVar(&opts.realm, "realm", "junglegaming", "realm that issues the tokens")
	set.StringVar(&opts.internalClient, "internal-client", "wallet-internal", "client that opens, reads and reconciles the wallets")
	set.StringVar(&opts.internalSecret, "internal-secret", "wallet-internal-local", "secret of that client")
	set.StringVar(&opts.providerClient, "provider-client", "provider-a", "client that sends the wagers, and the provider the body names")
	set.StringVar(&opts.providerSecret, "provider-secret", "provider-a-local", "secret of that client")
	set.StringVar(&opts.prometheus, "prometheus", "http://localhost:9095", "base address of the metric backend")
	set.IntVar(&opts.replicas, "replicas", 3, "replicas that must each decide arrivals")
	set.DurationVar(&opts.duration, "duration", 60*time.Second, "how long the load sends")
	set.IntVar(&opts.concurrency, "concurrency", 32, "workers sending at once, each on a connection of its own")
	set.IntVar(&opts.wallets, "wallets", 100, "wallets the load opens and spreads over")
	set.Uint64Var(&opts.seed, "seed", 1, "seed of the mix of wallets, kinds and amounts")
	set.StringVar(&opts.kill, "kill", killNone, "kill one replica halfway: graceful or forced, or nothing when empty")
	set.StringVar(&opts.kubectl, "kubectl", "kubectl", "kubectl binary the cluster target kills a pod with")
	set.StringVar(&opts.context, "context", "kind-junglegaming", "kubectl context of the cluster")
	set.StringVar(&opts.namespace, "namespace", "junglegaming", "namespace of the replicas in the cluster")
	set.StringVar(&opts.report, "report", ".quality/load/report.json", "machine-readable report, relative to root")
	set.DurationVar(&opts.drainWait, "drain-wait", 10*time.Minute, "how long the outbox has to drain after the window before the run fails")
	if err := set.Parse(args); err != nil {
		return options{}, err
	}
	return withTarget(opts)
}

// withTarget fills what the target decides and refuses what no run can use.
func withTarget(o options) (options, error) {
	chosen, ok := targets[o.target]
	if !ok {
		return options{}, fmt.Errorf("-target must be compose or cluster, got %q", o.target)
	}
	if err := bounded(o); err != nil {
		return options{}, err
	}
	o.address, o.replicaLabel = chosen.address, chosen.replica
	if !filepath.IsAbs(o.report) {
		o.report = filepath.Join(o.root, o.report)
	}
	return o, nil
}

// bounded refuses a count or a length that leaves nothing to measure.
func bounded(o options) error {
	switch {
	case o.duration <= 0:
		return fmt.Errorf("-duration must be positive, got %s", o.duration)
	case o.concurrency < 1:
		return fmt.Errorf("-concurrency must be at least 1, got %d", o.concurrency)
	case o.replicas < 1:
		return fmt.Errorf("-replicas must be at least 1, got %d", o.replicas)
	case o.wallets < 1:
		return fmt.Errorf("-wallets must be at least 1, got %d", o.wallets)
	case o.drainWait <= 0:
		return fmt.Errorf("-drain-wait must be positive, got %s", o.drainWait)
	case o.kill != killNone && o.kill != killGraceful && o.kill != killForced:
		return fmt.Errorf("-kill must be graceful, forced or empty, got %q", o.kill)
	}
	return nil
}
