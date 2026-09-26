package divergencewatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/junglegaming/backend-challenge-go/internal/app/reconcilewallet"
	"github.com/junglegaming/backend-challenge-go/internal/platform/fault"
	"github.com/junglegaming/backend-challenge-go/internal/platform/metrics"
)

// The reporter writes through the handler it was given and moves the series
// it was given, and nothing else of the process.
func TestNewReporter_logsAndCountsThroughWhatItWasGiven(t *testing.T) {
	t.Parallel()
	var written bytes.Buffer
	moved := series()
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), moved)
	reporter.Checked(context.Background(), reconcilewallet.Report{
		WalletID:    wallets(t, 1)[0],
		Divergences: []reconcilewallet.Divergence{reconcilewallet.SequenceGap},
	})
	if !strings.Contains(written.String(), "SEQUENCE_GAP") {
		t.Fatalf("log of the reporter = %q, want the line in the handler it was given", written.String())
	}
	if got := testutil.ToFloat64(moved.Divergences.WithLabelValues("watch", "SEQUENCE_GAP")); got != 1 {
		t.Fatalf("divergences{watch,SEQUENCE_GAP} on the series it was given = %v, want 1", got)
	}
}

// A consistent wallet counts as checked and leaves no line: there is nothing
// to name, and a line per wallet per sweep would drown the ones that matter.
func TestChecked_countsAConsistentWalletAndLeavesNoLine(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	moved := series()
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), moved)
	reporter.Checked(context.Background(), reconcilewallet.Report{WalletID: wallets(t, 1)[0], Consistent: true, StoredBalance: brl(t, "1000.00")})
	if written.Len() != 0 {
		t.Fatalf("log of a consistent wallet = %q, want nothing", written.String())
	}
	if got := testutil.ToFloat64(moved.WalletsChecked.WithLabelValues("watch")); got != 1 {
		t.Fatalf("wallets_checked{watch} after a consistent verdict = %v, want 1", got)
	}
	if got := testutil.CollectAndCount(moved.Divergences); got != 2*len(reconcilewallet.Vocabulary()) {
		t.Fatalf("divergence series = %d, want only the ones primed at zero", got)
	}
}

// A divergent wallet counts each token it was found with and leaves the line
// of the route: the wallet and the tokens, never a balance.
func TestChecked_logsTheDivergentWalletAndCountsEveryToken(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	moved := series()
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), moved)
	id := wallets(t, 1)[0]
	reporter.Checked(context.Background(), reconcilewallet.Report{
		WalletID:      id,
		StoredBalance: brl(t, "2000.00"),
		LedgerBalance: brl(t, "1250.00"),
		Difference:    brl(t, "750.00"),
		Divergences:   []reconcilewallet.Divergence{reconcilewallet.BalanceMismatch, reconcilewallet.ChainBreak},
	})
	assertLine(t, written.String(),
		[]string{`"walletId":"` + id.String() + `"`, "BALANCE_MISMATCH", "CHAIN_BREAK"},
		[]string{"2000.00", "1250.00", "750.00", "storedBalance", "ledgerBalance", "calculatedBalance", "difference"})
	assertCounted(t, moved, map[string]float64{"BALANCE_MISMATCH": 1, "CHAIN_BREAK": 1, "SEQUENCE_GAP": 0})
	if got := testutil.ToFloat64(moved.WalletsChecked.WithLabelValues("watch")); got != 1 {
		t.Fatalf("wallets_checked{watch} after a divergent verdict = %v, want 1", got)
	}
}

// assertLine pins what the line carries and what it must never carry.
func assertLine(t *testing.T, line string, wanted, banned []string) {
	t.Helper()
	for _, want := range wanted {
		if !strings.Contains(line, want) {
			t.Fatalf("divergence line = %q, want %q in it", line, want)
		}
	}
	for _, ban := range banned {
		if strings.Contains(line, ban) {
			t.Fatalf("divergence line = %q, want it without %q", line, ban)
		}
	}
}

// assertCounted reads the divergence series of the watcher, one per token.
func assertCounted(t *testing.T, moved *metrics.Settlement, want map[string]float64) {
	t.Helper()
	for token, count := range want {
		if got := testutil.ToFloat64(moved.Divergences.WithLabelValues("watch", token)); got != count {
			t.Fatalf("divergences{watch,%s} = %v, want %v", token, got, count)
		}
	}
}

// A turn the shutdown cut short is not a failure, and a line per turn cut would
// say it was.
func TestFailed_recordsNothingForATurnTheShutdownCutShort(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), series())
	reporter.Failed(context.Background(), "page the wallets", fmt.Errorf("page the wallets: %w", context.Canceled))
	if written.Len() != 0 {
		t.Fatalf("log of a cancelled turn = %q, want nothing", written.String())
	}
}

// The chain names where the failure came from and the frames say where it was
// first seen; a failure that crossed no boundary is captured at this border. A
// page that fails attempted no verdict, and counts none as failed.
func TestFailed_recordsTheChainAndTheFramesAndCountsNothing(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	moved := series()
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), moved)
	reporter.Failed(context.Background(), "page the wallets", errors.New("scan wallet id: connection reset"))
	line := written.String()
	if !strings.Contains(line, "scan wallet id: connection reset") || !strings.Contains(line, "divergencewatch.stackOf") {
		t.Fatalf("log = %q, want the chain and frames captured at this border", line)
	}
	if got := testutil.ToFloat64(moved.ReconciliationFailures.WithLabelValues("watch")); got != 0 {
		t.Fatalf("reconciliation_failures{watch} after a page that failed = %v, want 0", got)
	}
}

// A verdict that could not be produced leaves the line of the failure with the
// wallet, and counts one failure of the watcher and none of the route.
func TestUnverified_logsTheWalletAndCountsOneFailureOfTheWatcher(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	moved := series()
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), moved)
	id := wallets(t, 1)[0]
	reporter.Unverified(context.Background(), errors.New("rebuild ledger balance: overflow"), id)
	assertLine(t, written.String(),
		[]string{`"msg":"reconcile a wallet"`, `"walletId":"` + id.String() + `"`, "rebuild ledger balance: overflow", "divergencewatch.stackOf"},
		nil)
	if got := testutil.ToFloat64(moved.ReconciliationFailures.WithLabelValues("watch")); got != 1 {
		t.Fatalf("reconciliation_failures{watch} after a verdict that failed = %v, want 1", got)
	}
	if got := testutil.ToFloat64(moved.ReconciliationFailures.WithLabelValues("http")); got != 0 {
		t.Fatalf("reconciliation_failures{http} after a verdict of the watcher = %v, want 0", got)
	}
}

// A verdict the shutdown cut is neither logged nor counted: the process is
// leaving, and nothing about the wallet was learnt.
func TestUnverified_recordsNothingForAVerdictTheShutdownCutShort(t *testing.T) {
	t.Parallel()
	var written strings.Builder
	moved := series()
	reporter := NewReporter(slog.New(slog.NewJSONHandler(&written, nil)), moved)
	reporter.Unverified(context.Background(), fmt.Errorf("reconcile wallet: %w", context.Canceled), wallets(t, 1)[0])
	if written.Len() != 0 {
		t.Fatalf("log of a cancelled verdict = %q, want nothing", written.String())
	}
	if got := testutil.ToFloat64(moved.ReconciliationFailures.WithLabelValues("watch")); got != 0 {
		t.Fatalf("reconciliation_failures{watch} after a cancelled verdict = %v, want 0", got)
	}
}

// One failure keeps one stack: a chain that already crossed an I/O boundary
// carries its frames, and a chain with none has them captured here. The count
// of frames of a bare chain is 0 until this border takes them.
func TestStackOf_keepsTheFramesTheChainCarriesAndCapturesTheOnesItLacks(t *testing.T) {
	t.Parallel()
	seen := fault.Wrap("acquire connection", errors.New("connection refused"))
	if got := stackOf("reconcile a wallet", seen); !slices.Equal(got, fault.Stack(seen)) {
		t.Fatalf("frames of a chain that carries a stack = %v, want the %v it came with", got, fault.Stack(seen))
	}
	bare := errors.New("rebuild ledger balance: overflow")
	if carried := len(fault.Stack(bare)); carried != 0 {
		t.Fatalf("frames of a bare chain before the border = %d, want 0", carried)
	}
	if got := stackOf("reconcile a wallet", bare); len(got) == 0 {
		t.Fatalf("frames captured for a bare chain = %v, want them taken at this border", got)
	}
}
