//go:build integration

package scenarios

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// The third scenario of the statement: distinct wallets settle at the same time.
// A transaction outside the application holds one wallet for writing, and the
// bets of the others, spread over the fleet, settle while it does: the lock is
// the row of one wallet and never a lock of the table or of the process. The bet
// of the held wallet waits for that row and settles once it is let go.
func TestLockedWallet_doesNotHoldTheOthers(t *testing.T) {
	ctx, at := setUp(t)
	instances := at.launch(ctx, t, at.params.Instances, nil)
	db := connect(ctx, t)
	held := at.open(ctx, t, instances.at(0), fundedBalance)
	wallets := []owner{held}
	for index := range at.params.OtherWallets {
		wallets = append(wallets, at.open(ctx, t, instances.at(index+1), fundedBalance))
	}
	holding := holdRow(ctx, t, held.id)
	asks := make([]request, len(wallets))
	for index, each := range wallets {
		asks[index] = at.wager(instances.at(index), newKey(), each.bet(betAmount))
	}

	arrivals := release(ctx, asks)
	released := time.Now()
	for index := 1; index < len(asks); index++ {
		assertSettledWhileHeld(t, arrivals.answered(ctx, t, index))
	}
	t.Logf("%d other wallets settled %s after the release, while the row of %s was held", len(asks)-1, since(released), held.id)
	holding.awaitBlocked(ctx, t)
	if !arrivals.waiting(0) {
		t.Fatalf("the bet of the held wallet answered while its row was held: %s", arrivals.answered(ctx, t, 0).body)
	}
	t.Logf("the bet of the held wallet is blocked by backend %d and has not answered", holding.backend)
	holding.letGo(ctx, t)
	assertSettledWhileHeld(t, arrivals.answered(ctx, t, 0))
	t.Logf("the bet of the held wallet settled once the row was let go, %s after the release", since(released))
	assertEachDebitedOnce(ctx, t, db, wallets)
}

func assertEachDebitedOnce(ctx context.Context, t *testing.T, db store, wallets []owner) {
	t.Helper()
	for _, each := range wallets {
		if got := db.wallet(ctx, t, each.id); got != (stored{cents: 97500, version: 2}) {
			t.Errorf("wallet %s = %v, want 975.00 at version 2", each.id, got)
		}
	}
	t.Logf("all %d wallets: 975.00 at version 2", len(wallets))
}

func assertSettledWhileHeld(t *testing.T, answered answer) {
	t.Helper()
	if answered.status != http.StatusCreated {
		t.Fatalf("bet of a wallet = %d, want 201: %s", answered.status, answered.body)
	}
}

// heldRow is a transaction of the case that holds one wallet for writing, the way
// a submission does, and the backend it runs on.
type heldRow struct {
	tx      pgx.Tx
	db      store
	backend int32
}

// holdRow takes the row of the wallet on a connection of its own, so the reads of
// the case never run inside the transaction that holds it.
func holdRow(ctx context.Context, t *testing.T, walletID string) heldRow {
	t.Helper()
	db := connect(ctx, t)
	var backend int32
	if err := db.conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&backend); err != nil {
		t.Fatalf("read the backend of the holder = %v, want nil", err)
	}
	tx, err := db.conn.Begin(ctx)
	if err != nil {
		t.Fatalf("begin the holding transaction = %v, want nil", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.WithoutCancel(ctx)) })
	if _, err := tx.Exec(ctx, "SELECT id FROM wallets WHERE id = $1 FOR UPDATE", walletID); err != nil {
		t.Fatalf("hold the wallet = %v, want nil", err)
	}
	return heldRow{tx: tx, db: connect(ctx, t), backend: backend}
}

// awaitBlocked waits until some backend is blocked by the holder. That is what
// tells a bet waiting for the row from a bet that has merely not arrived yet.
func (h heldRow) awaitBlocked(ctx context.Context, t *testing.T) {
	t.Helper()
	until(ctx, t, "the bet of the held wallet to wait for its row", func() bool {
		const query = "SELECT count(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))"
		var blocked int64
		if err := h.db.conn.QueryRow(ctx, query, h.backend).Scan(&blocked); err != nil {
			t.Fatalf("read who the holder blocks = %v, want nil", err)
		}
		return blocked > 0
	})
}

// letGo undoes the holding transaction, which is what releases the row.
func (h heldRow) letGo(ctx context.Context, t *testing.T) {
	t.Helper()
	if err := h.tx.Rollback(ctx); err != nil {
		t.Fatalf("let the wallet go = %v, want nil", err)
	}
}
