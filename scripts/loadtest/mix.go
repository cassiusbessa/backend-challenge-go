package main

import (
	"math/rand/v2"
)

// The mix of the load. A quarter of the arrivals falls on four hot wallets, so
// replicas fight over the same balance; the three kinds are the ones the route
// settles on its own, with no reference to wait for; and one arrival in twenty
// sends an earlier operation again, key and body alike.
const (
	hotWallets  = 4
	hotShare    = 0.25
	betShare    = 0.50
	winShare    = 0.40
	repeatShare = 0.05
	minCents    = 100
	maxCents    = 500
)

// The kinds the load sends.
const (
	kindBet  = "BET"
	kindWin  = "WIN"
	kindLoss = "LOSS"
)

// draw is one new operation before it has an identity: which wallet, which
// kind and how much.
type draw struct {
	wallet int
	kind   string
	cents  int64
}

// mixer draws the operations of one worker. Each worker has a sequence of its
// own from the seed, so a run is repeatable however the workers interleave.
type mixer struct {
	rng     *rand.Rand
	wallets int
}

func newMixer(seed uint64, worker, wallets int) *mixer {
	return &mixer{rng: rand.New(rand.NewPCG(seed, uint64(worker))), wallets: wallets} //nolint:gosec // a seeded mix is what makes a run repeatable, and nothing drawn is secret
}

// repeats reports whether the next arrival sends an earlier operation again.
func (m *mixer) repeats() bool {
	return m.rng.Float64() < repeatShare
}

// pick answers which of the earlier operations is sent again.
func (m *mixer) pick(earlier int) int {
	return m.rng.IntN(earlier)
}

// next draws a new operation. A LOSS carries zero, which is the only amount the
// schema accepts for it.
func (m *mixer) next() draw {
	chosen := draw{wallet: m.wallet(), kind: m.kind()}
	if chosen.kind != kindLoss {
		chosen.cents = minCents + m.rng.Int64N(maxCents-minCents+1)
	}
	return chosen
}

// wallet answers the index of the wallet, the hot ones first. With no more
// wallets than the hot set, every wallet is hot.
func (m *mixer) wallet() int {
	hot := min(hotWallets, m.wallets)
	if hot == m.wallets || m.rng.Float64() < hotShare {
		return m.rng.IntN(hot)
	}
	return hot + m.rng.IntN(m.wallets-hot)
}

func (m *mixer) kind() string {
	switch roll := m.rng.Float64(); {
	case roll < betShare:
		return kindBet
	case roll < betShare+winShare:
		return kindWin
	}
	return kindLoss
}
