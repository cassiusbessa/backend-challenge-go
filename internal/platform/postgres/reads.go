package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/junglegaming/backend-challenge-go/internal/app/storage"
	"github.com/junglegaming/backend-challenge-go/internal/domain/identity"
	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

const selectWallet = `
SELECT id, player_id, currency, balance_cents, version, created_at, updated_at
  FROM wallets
 WHERE id = $1`

// Reads answers read models from the pool. A read opens no transaction and
// writes nothing. The zero value is not used: NewReads is the only constructor.
type Reads struct {
	source *Pool
}

func NewReads(source *Pool) *Reads {
	return &Reads{source: source}
}

// Wallet answers the stored wallet, or ErrWalletNotFound when the identity is
// not a wallet of this context.
func (r *Reads) Wallet(ctx context.Context, id identity.WalletID) (storage.WalletView, error) {
	pool, err := r.source.Querier()
	if err != nil {
		return storage.WalletView{}, wrap("acquire pool", err)
	}
	var found walletRow
	err = pool.QueryRow(ctx, selectWallet, id.String()).Scan(
		&found.id, &found.playerID, &found.currency,
		&found.cents, &found.version, &found.createdAt, &found.updatedAt,
	)
	if err != nil {
		return storage.WalletView{}, readFailure(err)
	}
	return found.view()
}

func readFailure(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return storage.ErrWalletNotFound
	}
	return wrap("read wallet", err)
}

// walletRow is the row as PostgreSQL hands it over, before the domain types
// take it back.
type walletRow struct {
	id        string
	playerID  string
	currency  string
	cents     int64
	version   int64
	createdAt time.Time
	updatedAt time.Time
}

func (r walletRow) view() (storage.WalletView, error) {
	id, err := identity.ParseWalletID(r.id)
	if err != nil {
		return storage.WalletView{}, wrap("read wallet identity", err)
	}
	playerID, err := identity.ParsePlayerID(r.playerID)
	if err != nil {
		return storage.WalletView{}, wrap("read player identity", err)
	}
	balance, err := r.balance()
	if err != nil {
		return storage.WalletView{}, wrap("read wallet balance", err)
	}
	return storage.WalletView{
		ID:        id,
		PlayerID:  playerID,
		Balance:   balance,
		Version:   r.version,
		CreatedAt: r.createdAt,
		UpdatedAt: r.updatedAt,
	}, nil
}

func (r walletRow) balance() (money.Money, error) {
	currency, err := money.ParseCurrency(r.currency)
	if err != nil {
		return money.Money{}, err
	}
	return money.FromCents(r.cents, currency)
}
