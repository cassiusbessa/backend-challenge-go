package main

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// A run over a service that settles every arrival once closes every wallet,
// replays and all.
func TestRun_passesOverAConsistentService(t *testing.T) {
	t.Parallel()
	fake, server := newFakeService(t)
	if err := run(context.Background(), optionsFor(t, server), &strings.Builder{}); err != nil {
		t.Fatalf("run over a consistent service = %v, want nil", err)
	}
	if fake.arrivals == 0 {
		t.Fatalf("arrivals received = %d, want the run to have sent", fake.arrivals)
	}
}

// A wallet that does not close is named with the field, what was wanted and
// what was read.
func TestCheckWallet_namesTheWalletTheFieldTheWantedAndTheRead(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		bend func(*fakeService)
		want string
	}{
		{name: "a lost movement", bend: func(f *fakeService) { f.drift = -325 }, want: "balance = 9996.75, want 10000.00"},
		{name: "a version moved twice", bend: func(f *fakeService) { f.versionDrift = 1 }, want: "version = 2, want 1"},
		{name: "an entry written twice", bend: func(f *fakeService) { f.entriesDrift = 1 }, want: "checkedEntries = 2, want 1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fake, server := newFakeService(t)
			o := optionsFor(t, server)
			o.wallets = 1
			l := newLoad(o)
			if err := l.open(context.Background()); err != nil {
				t.Fatalf("open = %v, want nil", err)
			}
			tc.bend(fake)
			findings := l.check(context.Background())
			if len(findings) == 0 || !strings.Contains(findings[0], "wallet "+l.wallets[0].id+": "+tc.want) {
				t.Fatalf("findings of %s = %v, want the wallet named with %q", tc.name, findings, tc.want)
			}
		})
	}
}

// A wallet the reads cannot answer is a finding too, and not a pass.
func TestCheckWallet_namesTheReadThatDidNotAnswer(t *testing.T) {
	t.Parallel()
	_, server := newFakeService(t)
	l := newLoad(optionsFor(t, server))
	findings := l.checkWallet(context.Background(), "00000000-0000-4000-8000-000000000000", tally{})
	if len(findings) != 1 || !strings.Contains(findings[0], "read answered 404, want 200") {
		t.Fatalf("findings of a wallet that does not exist = %v, want the read named", findings)
	}
}

// A replay is the same operation: it is counted once, and a rejection and a
// LOSS move nothing the verdict reads.
func TestTallies_countsEachOperationOnceByItsFirstDecision(t *testing.T) {
	t.Parallel()
	l := newLoad(options{})
	l.wallets = []wallet{{id: "w-0"}}
	bet := &operation{key: "k-bet", kind: kindBet, cents: 300}
	win := &operation{key: "k-win", kind: kindWin, cents: 500}
	loss := &operation{key: "k-loss", kind: kindLoss}
	refused := &operation{key: "k-refused", kind: kindBet, cents: 100}
	l.operations = []*operation{bet, win, loss, refused}
	l.decide(bet, answer{class: decided, status: statusProcessed})
	l.decide(bet, answer{class: decided, status: statusProcessed, replay: true})
	l.decide(win, answer{class: decided, status: statusProcessed})
	l.decide(loss, answer{class: decided, status: statusProcessed})
	l.decide(refused, answer{class: decided, status: statusRejected, failureCode: "INSUFFICIENT_FUNDS"})
	if got := l.tallies()[0]; got != (tally{cents: 200, moves: 2}) {
		t.Fatalf("tally = %+v, want +2.00 over 2 movements: the replay once, the LOSS and the rejection none", got)
	}
}

// Only the two-place decimal the contract writes is read; anything else is
// refused rather than rounded.
func TestCentsOf_readsOnlyTheDecimalOfTwoPlaces(t *testing.T) {
	t.Parallel()
	if got, err := centsOf("10000.25"); err != nil || got != 1_000_025 {
		t.Fatalf("centsOf(10000.25) = %d, %v, want 1000025 and nil", got, err)
	}
	for _, refused := range []string{"10", "1.5", "1.234", "-1.00", "", "a.bc"} {
		if _, err := centsOf(refused); err == nil {
			t.Fatalf("centsOf(%q) = nil error, want it refused", refused)
		}
	}
}

// A read that failed before an answer names the wallet and the read.
func TestReadFinding_namesTheReadAndWhatWentWrong(t *testing.T) {
	t.Parallel()
	if got := readFinding("w-1", "reconciliation", http.StatusOK, nil); got != "" {
		t.Fatalf("finding of a read that answered 200 = %q, want none", got)
	}
	if got := readFinding("w-1", "reconciliation", 0, context.DeadlineExceeded); !strings.Contains(got, "wallet w-1: reconciliation: context deadline exceeded") {
		t.Fatalf("finding of a read that timed out = %q, want the wallet, the read and the failure", got)
	}
}
