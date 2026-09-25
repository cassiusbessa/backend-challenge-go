//go:build integration

// The settlement as it is driven in this suite: a wallet, the operations over
// it, and the relay that moves what those commits wrote out to the topic.
package events

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/junglegaming/backend-challenge-go/internal/app/openwallet"
	"github.com/junglegaming/backend-challenge-go/internal/app/referencewait"
	"github.com/junglegaming/backend-challenge-go/internal/app/relayoutbox"
	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/app/submitwager"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
	"github.com/junglegaming/backend-challenge-go/internal/domain/wager"
	"github.com/junglegaming/backend-challenge-go/internal/platform/clock"
	"github.com/junglegaming/backend-challenge-go/internal/platform/mint"
	"github.com/junglegaming/backend-challenge-go/internal/platform/postgres"
)

// lease is the one every case of this suite claims under. It is short so that a
// case about an expired lease does not wait out the production default.
const lease = 5 * time.Second

// The amounts every case runs on: a wallet born with money and one bet over it,
// which leaves the balance at 975.00.
const (
	openingBalance = "1000.00"
	betAmount      = "25.00"
	afterTheBet    = "975.00"
)

// settlement is the write side of the suite: the two use cases that commit and
// the queue the relay works.
type settlement struct {
	pool   *postgres.Pool
	queue  *postgres.OutboxQueue
	opener *openwallet.Service
	wagers *submitwager.Service
}

func stack(t *testing.T) (context.Context, *settlement) {
	t.Helper()
	ctx, pool := open(t)
	unit := postgres.NewUnitOfWork(pool)
	reads := postgres.NewReads(pool)
	schedule := referencewait.New(15*time.Minute, referencewait.FullJitter)
	return ctx, &settlement{
		pool:   pool,
		queue:  postgres.NewOutboxQueue(pool),
		opener: openwallet.New(unit, mint.UUIDv7{}, clock.UTC{}),
		wagers: submitwager.New(unit, reads, mint.UUIDv7{}, clock.UTC{}, schedule),
	}
}

// openWallet opens a wallet with money in it, which is the shape every case of
// this suite starts from: the birth writes its own two events, and the bet that
// follows writes two more behind them.
func (s *settlement) openWallet(ctx context.Context, t *testing.T) openwallet.Result {
	t.Helper()
	player, err := identity.ParsePlayerID(newID())
	if err != nil {
		t.Fatalf("ParsePlayerID = %v, want nil", err)
	}
	opened, err := s.opener.Open(ctx, openwallet.Command{PlayerID: player, InitialBalance: brl(t, openingBalance)})
	if err != nil {
		t.Fatalf("Open = %v, want nil", err)
	}
	return opened
}

func (s *settlement) bet(ctx context.Context, t *testing.T, owner openwallet.Result) submitwager.Result {
	t.Helper()
	settled, err := s.wagers.Submit(ctx, command(t, owner, wager.KindBet, betAmount))
	if err != nil {
		t.Fatalf("Submit of a bet = %v, want nil", err)
	}
	return settled
}

func command(t *testing.T, owner openwallet.Result, kind wager.Kind, amount string) submitwager.Command {
	t.Helper()
	external := newID()
	return submitwager.Command{
		ProviderID:     tokenOf(t, identity.ParseProviderID, "provider-a"),
		ExternalID:     tokenOf(t, identity.ParseExternalTransactionID, external),
		IdempotencyKey: tokenOf(t, identity.ParseIdempotencyKey, "key-"+external),
		PlayerID:       owner.PlayerID,
		WalletID:       owner.WalletID,
		RoundID:        tokenOf(t, identity.ParseRoundID, "round-"+external),
		GameID:         tokenOf(t, identity.ParseGameID, "crash"),
		Kind:           kind,
		Amount:         brl(t, amount),
	}
}

func tokenOf[T any](t *testing.T, parse func(string) (T, error), text string) T {
	t.Helper()
	parsed, err := parse(text)
	if err != nil {
		t.Fatalf("parse of %q = %v, want nil", text, err)
	}
	return parsed
}

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, "BRL")
	if err != nil {
		t.Fatalf("money.Parse of %s = %v, want nil", amount, err)
	}
	return parsed
}

// row is one outbox row as this suite reads it back.
type row struct {
	EventID       string
	EventType     string
	TransactionID string
	Published     bool
	Dead          bool
	Payload       string
	Attempts      int64
}

// rows answers the outbox of one wallet, oldest first, which is the order the
// relay is supposed to publish them in.
func (s *settlement) rows(ctx context.Context, t *testing.T, owner identity.WalletID) []row {
	t.Helper()
	querier := s.querier(t)
	const query = `
SELECT event_id, event_type, payload ->> 'data', published_at IS NOT NULL,
       dead_at IS NOT NULL, payload::text, attempt_count
  FROM outbox_events WHERE wallet_id = $1 ORDER BY created_at, event_id`
	found, err := querier.Query(ctx, query, owner.String())
	if err != nil {
		t.Fatalf("query the outbox = %v, want nil", err)
	}
	defer found.Close()
	return scanRows(t, found)
}

func scanRows(t *testing.T, found interface {
	Next() bool
	Scan(...any) error
	Err() error
}) []row {
	t.Helper()
	var out []row
	for found.Next() {
		var each row
		var data string
		if err := found.Scan(&each.EventID, &each.EventType, &data, &each.Published, &each.Dead, &each.Payload, &each.Attempts); err != nil {
			t.Fatalf("read an outbox row = %v, want nil", err)
		}
		each.TransactionID = transactionOf(t, data)
		out = append(out, each)
	}
	if err := found.Err(); err != nil {
		t.Fatalf("read the outbox = %v, want nil", err)
	}
	return out
}

func transactionOf(t *testing.T, data string) string {
	t.Helper()
	var out struct {
		TransactionID string `json:"transactionId"`
	}
	if err := json.Unmarshal([]byte(data), &out); err != nil {
		t.Fatalf("read the data of an event = %v, want nil", err)
	}
	return out.TransactionID
}

// of answers the rows of one transaction, which is how a case names the two
// events of a single commit among the ones the wallet already carried.
func of(rows []row, transaction string) []row {
	var mine []row
	for _, each := range rows {
		if each.TransactionID == transaction {
			mine = append(mine, each)
		}
	}
	return mine
}

// relay builds one replica of the relay over a publisher of the case.
func (s *settlement) relay(t *testing.T, sender relayoutbox.Publisher) *relayoutbox.Service {
	t.Helper()
	return relayoutbox.New(s.queue, sender, noSpan, clock.UTC{}, quiet(), lease)
}

// drain works the queue until it has nothing left for this wallet, the way the
// runner would over several ticks.
func (s *settlement) drain(ctx context.Context, t *testing.T, service *relayoutbox.Service, owner identity.WalletID) {
	t.Helper()
	for range drainTurns {
		due := s.due(ctx, t, owner)
		if len(due) == 0 {
			return
		}
		for _, candidate := range due {
			relay(ctx, t, service, candidate)
		}
	}
	t.Fatalf("the outbox of the wallet still had candidates after %d turns", drainTurns)
}

// drainTurns bounds the loop: one turn per event of the wallet is generous, and
// a queue that does not empty is a case failing rather than a case waiting.
const drainTurns = 20

// due answers the candidates of one wallet. The suite shares the database with
// every other case, so the scan is read back filtered.
func (s *settlement) due(ctx context.Context, t *testing.T, owner identity.WalletID) []storage.OutboxCandidate {
	t.Helper()
	all, err := s.queue.Due(ctx, 500)
	if err != nil {
		t.Fatalf("Due = %v, want nil", err)
	}
	var mine []storage.OutboxCandidate
	for _, candidate := range all {
		if candidate.WalletID == owner {
			mine = append(mine, candidate)
		}
	}
	return mine
}

// claimOne takes the row at the head of the wallet under a lease, which is what
// a replica that is about to publish holds.
func (s *settlement) claimOne(ctx context.Context, t *testing.T, owner identity.WalletID) storage.OutboxRow {
	t.Helper()
	due := s.due(ctx, t, owner)
	if len(due) == 0 {
		t.Fatalf("candidates of the wallet = %d, want the one at the head of it", len(due))
	}
	claimed, err := s.queue.Claim(ctx, due[0].EventID, lease)
	if err != nil {
		t.Fatalf("Claim = %v, want nil", err)
	}
	return claimed
}

// release puts a claimed row back on the queue, which is what a replica that
// was stopped mid-turn leaves behind once its lease runs out.
func (s *settlement) release(ctx context.Context, t *testing.T, claimed storage.OutboxRow) {
	t.Helper()
	if err := s.queue.Reschedule(ctx, claimed.EventID, claimed.LeaseToken, timeOf(t)); err != nil {
		t.Fatalf("Reschedule = %v, want nil", err)
	}
}

// expire moves the lease of a row into the past, which is what a replica that
// stopped without confirming leaves behind.
func (s *settlement) expire(ctx context.Context, t *testing.T, id identity.EventID) {
	t.Helper()
	s.exec(ctx, t, `UPDATE outbox_events SET lease_until = now() - interval '1 second' WHERE event_id = $1`, id.String())
}

// attempt takes one turn over a named row, claiming it again each time: a row
// that came back on the backoff is due now, because the clock of this suite is
// the wall one and the case does not wait out the interval.
func (s *settlement) attempt(ctx context.Context, t *testing.T, service *relayoutbox.Service, owner identity.WalletID, id string) {
	t.Helper()
	parsed, err := identity.ParseEventID(id)
	if err != nil {
		t.Fatalf("ParseEventID = %v, want nil", err)
	}
	s.exec(ctx, t, `UPDATE outbox_events SET next_attempt_at = now() - interval '1 second' WHERE event_id = $1`, id)
	relay(ctx, t, service, storage.OutboxCandidate{EventID: parsed, WalletID: owner})
}

func (s *settlement) exec(ctx context.Context, t *testing.T, statement string, args ...any) {
	t.Helper()
	if _, err := s.querier(t).Exec(ctx, statement, args...); err != nil {
		t.Fatalf("write against the outbox = %v, want nil", err)
	}
}

func (s *settlement) rowOf(ctx context.Context, t *testing.T, owner identity.WalletID, id string) row {
	t.Helper()
	for _, each := range s.rows(ctx, t, owner) {
		if each.EventID == id {
			return each
		}
	}
	t.Fatalf("the wallet carries no event %s, want the one the case named", id)
	return row{}
}

// assertUnchanged reads the financial tables of the wallet back: an operation
// the database refused leaves the balance, the transactions and the ledger
// exactly as the operations that went through left them.
func (s *settlement) assertUnchanged(ctx context.Context, t *testing.T, owner identity.WalletID, balance string, transactions, entries int64) {
	t.Helper()
	const query = `
SELECT (SELECT balance_cents FROM wallets WHERE id = $1),
       (SELECT count(*) FROM wager_transactions WHERE wallet_id = $1),
       (SELECT count(*) FROM ledger_entries WHERE wallet_id = $1)`
	var cents, recorded, moved int64
	if err := s.querier(t).QueryRow(ctx, query, owner.String()).Scan(&cents, &recorded, &moved); err != nil {
		t.Fatalf("read the tables of the wallet = %v, want nil", err)
	}
	if cents != brl(t, balance).Cents() {
		t.Fatalf("balance = %d cents, want %s", cents, balance)
	}
	if recorded != transactions || moved != entries {
		t.Fatalf("transactions and entries = %d and %d, want %d and %d", recorded, moved, transactions, entries)
	}
}

// querier is the open pool of the suite, which every read and write here goes
// through.
func (s *settlement) querier(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := s.pool.Querier()
	if err != nil {
		t.Fatalf("acquire pool = %v, want nil", err)
	}
	return pool
}

// timeOf is the instant a write of this suite is stamped with. The relay stamps
// with the clock of the process, and so does a case that writes beside it.
func timeOf(t *testing.T) time.Time {
	t.Helper()
	return clock.UTC{}.Now()
}

// publishTheOneAhead asserts the wallet offers exactly the row the case expects
// at the head of it, and relays that one.
func (s *settlement) publishTheOneAhead(ctx context.Context, t *testing.T, service *relayoutbox.Service, owner identity.WalletID, expected string) {
	t.Helper()
	due := s.due(ctx, t, owner)
	if len(due) != 1 {
		t.Fatalf("candidates of the wallet = %d, want exactly the one ahead", len(due))
	}
	if due[0].EventID.String() != expected {
		t.Fatalf("candidate = %s, want %s, the oldest still pending", due[0].EventID, expected)
	}
	relay(ctx, t, service, due[0])
}

// relay takes one turn over one candidate. Nothing the relay decides about a
// row is a failure of the turn, so every case here expects nil back.
func relay(ctx context.Context, t *testing.T, service *relayoutbox.Service, candidate storage.OutboxCandidate) {
	t.Helper()
	if err := service.Relay(ctx, candidate); err != nil {
		t.Fatalf("Relay = %v, want nil", err)
	}
}
