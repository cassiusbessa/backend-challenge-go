package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/platform/config"
)

func TestView_takesTheRowBackIntoDomainTypes(t *testing.T) {
	t.Parallel()
	view, err := rowOf().view()
	if err != nil {
		t.Fatalf("view = %v, want nil", err)
	}
	if view.Balance.Amount() != "1000.00" {
		t.Fatalf("balance = %s, want 1000.00", view.Balance.Amount())
	}
	if view.Balance.Currency().Code() != "BRL" {
		t.Fatalf("currency = %s, want BRL", view.Balance.Currency().Code())
	}
	if view.Version != 3 {
		t.Fatalf("version = %d, want 3", view.Version)
	}
	if view.ID.String() != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("wallet = %s, want the stored identity", view.ID)
	}
}

func TestView_refusesARowTheDomainCannotAccept(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		mutil func(*walletRow)
	}{
		{name: "a wallet out of format is refused", mutil: func(r *walletRow) { r.id = "not-a-uuid" }},
		{name: "a player out of format is refused", mutil: func(r *walletRow) { r.playerID = "not-a-uuid" }},
		{name: "a currency outside ISO 4217 is refused", mutil: func(r *walletRow) { r.currency = "BRLL" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := rowOf()
			tc.mutil(&row)
			if _, err := row.view(); err == nil {
				t.Fatalf("view = nil, want an error for %s", tc.name)
			}
		})
	}
}

func TestWallet_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	id, err := identity.ParseWalletID("11111111-1111-4111-8111-111111111111")
	if err != nil {
		t.Fatalf("ParseWalletID = %v, want nil", err)
	}
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	_, err = reads.Wallet(context.Background(), id)
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Wallet = %v, want %v", err, ErrPoolClosed)
	}
}

func rowOf() walletRow {
	at := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	return walletRow{
		id:        "11111111-1111-4111-8111-111111111111",
		playerID:  "22222222-2222-4222-8222-222222222222",
		currency:  "BRL",
		cents:     100000,
		version:   3,
		createdAt: at,
		updatedAt: at,
	}
}

// Every read asks the pool for a querier first, and a closed pool is the one
// refusal a read can answer without a database behind it.
func TestTransaction_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	_, err := reads.Transaction(context.Background(), transactionIdentity(t), providerIdentity(t))
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("Transaction = %v, want %v", err, ErrPoolClosed)
	}
}

func TestTransactionByKey_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	_, err := reads.TransactionByKey(context.Background(), providerIdentity(t), keyIdentity(t))
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("TransactionByKey = %v, want %v", err, ErrPoolClosed)
	}
}

func transactionIdentity(t *testing.T) identity.TransactionID {
	t.Helper()
	parsed, err := identity.ParseTransactionID(rowTransaction)
	if err != nil {
		t.Fatalf("ParseTransactionID = %v, want nil", err)
	}
	return parsed
}

func providerIdentity(t *testing.T) identity.ProviderID {
	t.Helper()
	parsed, err := identity.ParseProviderID(rowProvider)
	if err != nil {
		t.Fatalf("ParseProviderID = %v, want nil", err)
	}
	return parsed
}

func keyIdentity(t *testing.T) identity.IdempotencyKey {
	t.Helper()
	parsed, err := identity.ParseIdempotencyKey("key-1")
	if err != nil {
		t.Fatalf("ParseIdempotencyKey = %v, want nil", err)
	}
	return parsed
}

// The scan of the queue answers nothing when the process has not opened the pool,
// and it says so as the failure it is rather than an empty queue: a worker told
// there is no work would sit idle over a queue it never read.
func TestDueWaits_answersTheFailureWhenThePoolIsNotOpen(t *testing.T) {
	t.Parallel()
	due, err := NewReads(NewPool(config.Config{})).DueWaits(context.Background(), scanStamp(), 10)
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("DueWaits over a pool that is not open = %v, want %v", err, ErrPoolClosed)
	}
	if due != nil {
		t.Fatalf("candidates beside the closed pool = %v, want none", due)
	}
}

// Each row of the scan comes back as the pair the decision needs: the wait to
// claim and the wallet to lock before it.
func TestScanCandidates_answersThePairsTheDecisionLocksInOrder(t *testing.T) {
	t.Parallel()
	due, err := scanCandidates(&stubRows{rows: [][2]string{
		{rowTransaction, rowWallet},
		{"44444444-4444-4444-8444-444444444444", rowWallet},
	}})
	if err != nil {
		t.Fatalf("scanCandidates = %v, want nil", err)
	}
	if len(due) != 2 {
		t.Fatalf("candidates = %d, want 2", len(due))
	}
	if due[0].TransactionID.String() != rowTransaction || due[0].WalletID.String() != rowWallet {
		t.Fatalf("first candidate = %+v, want the transaction and the wallet of the row", due[0])
	}
}

// A walk that failed is not a short queue: answering the rows read so far would
// have the worker take a partial scan for the whole of it.
func TestScanCandidates_answersTheFailureOfTheWalkAndNoCandidates(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset by peer")
	due, err := scanCandidates(&stubRows{rows: [][2]string{{rowTransaction, rowWallet}}, walk: broken})
	if !errors.Is(err, broken) {
		t.Fatalf("scanCandidates over a broken walk = %v, want %v", err, broken)
	}
	if due != nil {
		t.Fatalf("candidates beside the broken walk = %v, want none", due)
	}
}

// A row this context did not write is refused whole, with the operation named in
// the chain: a wait whose identities do not parse is not a wait to decide.
func TestScanCandidate_refusesARowOutsideTheVocabularyOfTheDomain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		row  [2]string
	}{
		{name: "a transaction out of format is refused", row: [2]string{"not-a-uuid", rowWallet}},
		{name: "a wallet out of format is refused", row: [2]string{rowTransaction, "not-a-uuid"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := scanCandidate(&stubRows{rows: [][2]string{tc.row}, at: -1})
			if err == nil {
				t.Fatalf("scanCandidate of %s = nil, want a refusal", tc.name)
			}
			if !strings.Contains(err.Error(), "read reference wait row") {
				t.Fatalf("refusal of %s = %q, want the operation named in the chain", tc.name, err.Error())
			}
		})
	}
}

// A row the driver could not hand over is the failure of the read, and the
// operation is what places it: the walk and the scan of one row look alike in a
// log line otherwise.
func TestScanCandidate_answersTheFailureOfTheDriver(t *testing.T) {
	t.Parallel()
	broken := errors.New("connection reset by peer")
	_, err := scanCandidate(&stubRows{scan: broken, at: -1})
	if !errors.Is(err, broken) {
		t.Fatalf("scanCandidate over a broken row = %v, want %v", err, broken)
	}
	if !strings.Contains(err.Error(), "read reference wait row") {
		t.Fatalf("failure of the driver = %q, want the operation named in the chain", err.Error())
	}
}

// stubRows is the result set of one case: the pairs it hands over, and the
// failures the driver may answer while it is walked or scanned.
type stubRows struct {
	pgx.Rows
	rows [][2]string
	at   int
	scan error
	walk error
}

func (r *stubRows) Next() bool {
	if r.at >= len(r.rows) {
		return false
	}
	r.at++
	return r.at <= len(r.rows)
}

func (r *stubRows) Scan(into ...any) error {
	if r.scan != nil {
		return r.scan
	}
	row := r.rows[max(r.at-1, 0)]
	*(into[0].(*string)) = row[0]
	*(into[1].(*string)) = row[1]
	return nil
}

func (r *stubRows) Err() error {
	return r.walk
}

func (r *stubRows) Close() {}

func scanStamp() time.Time {
	return time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
}

func TestOldestWait_refusesWhileTheSharedPoolIsClosed(t *testing.T) {
	t.Parallel()
	reads := NewReads(NewPool(config.Config{DatabaseURL: unreachable}))
	_, err := reads.OldestWait(context.Background(), time.Now())
	if !errors.Is(err, ErrPoolClosed) {
		t.Fatalf("OldestWait = %v, want %v", err, ErrPoolClosed)
	}
}

// Nothing waiting is an age of zero, and a wait is measured from its entry. An
// entry stamped after the instant asked about reads as no age rather than a
// negative one.
func TestAgeOf_measuresTheWaitFromItsEntry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.September, 24, 12, 0, 0, 0, time.UTC)
	if got := ageOf(now, nil); got != 0 {
		t.Fatalf("age with nothing waiting = %s, want 0", got)
	}
	entered := now.Add(-90 * time.Second)
	if got := ageOf(now, &entered); got != 90*time.Second {
		t.Fatalf("age of a wait entered 90s ago = %s, want 1m30s", got)
	}
	ahead := now.Add(time.Second)
	if got := ageOf(now, &ahead); got != 0 {
		t.Fatalf("age of a wait stamped ahead of the clock = %s, want 0", got)
	}
}
