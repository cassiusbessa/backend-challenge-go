package main

import (
	"math"
	"slices"
	"testing"
)

// Over many draws of a fixed seed, the mix lands on the proportions of the
// design within a tolerance: a quarter on the four hot wallets, half BET, two
// fifths WIN, the rest LOSS, and one arrival in twenty repeated.
func TestMixer_drawsTheProportionsOfTheDesign(t *testing.T) {
	t.Parallel()
	const draws = 200_000
	mix := newMixer(7, 0, 100)
	var hot, repeated int
	kinds := map[string]int{}
	for range draws {
		if mix.repeats() {
			repeated++
		}
		chosen := mix.next()
		if chosen.wallet < hotWallets {
			hot++
		}
		kinds[chosen.kind]++
	}
	for name, share := range map[string]struct{ got, want float64 }{
		"hot wallets": {got: float64(hot) / draws, want: hotShare},
		"BET":         {got: float64(kinds[kindBet]) / draws, want: betShare},
		"WIN":         {got: float64(kinds[kindWin]) / draws, want: winShare},
		"LOSS":        {got: float64(kinds[kindLoss]) / draws, want: 1 - betShare - winShare},
		"repeats":     {got: float64(repeated) / draws, want: repeatShare},
	} {
		if math.Abs(share.got-share.want) > 0.01 {
			t.Fatalf("share of %s = %.4f, want %.2f within 0.01", name, share.got, share.want)
		}
	}
}

// A LOSS carries zero, and every other kind an amount from 1.00 to 5.00.
func TestNext_drawsTheAmountOfTheKind(t *testing.T) {
	t.Parallel()
	mix := newMixer(3, 1, 10)
	for range 10_000 {
		chosen := mix.next()
		if chosen.kind == kindLoss && chosen.cents != 0 {
			t.Fatalf("amount of a LOSS = %d cents, want 0", chosen.cents)
		}
		if chosen.kind != kindLoss && (chosen.cents < minCents || chosen.cents > maxCents) {
			t.Fatalf("amount of a %s = %d cents, want from %d to %d", chosen.kind, chosen.cents, minCents, maxCents)
		}
	}
}

// The same seed and worker draw the same sequence, and another worker draws
// another: a run is repeatable however the workers interleave.
func TestNewMixer_repeatsTheSequenceOfASeedAndWorker(t *testing.T) {
	t.Parallel()
	sequence := func(worker int) []draw {
		mix := newMixer(11, worker, 50)
		out := make([]draw, 0, 64)
		for range 64 {
			out = append(out, mix.next())
		}
		return out
	}
	if !slices.Equal(sequence(2), sequence(2)) {
		t.Fatalf("two sequences of seed 11 and worker 2 differ, want them equal")
	}
	if slices.Equal(sequence(2), sequence(3)) {
		t.Fatalf("sequences of workers 2 and 3 are equal, want each worker its own")
	}
}

// With no more wallets than the hot set, every wallet is hot and none is out
// of range.
func TestWallet_treatsEveryWalletAsHotWhenThereAreNoMore(t *testing.T) {
	t.Parallel()
	mix := newMixer(5, 0, 3)
	for range 1_000 {
		if got := mix.wallet(); got < 0 || got >= 3 {
			t.Fatalf("wallet drawn among 3 = %d, want from 0 to 2", got)
		}
	}
}

// The earlier operation picked to repeat is one of those already sent.
func TestPick_answersOneOfTheEarlierOperations(t *testing.T) {
	t.Parallel()
	mix := newMixer(9, 0, 10)
	for range 1_000 {
		if got := mix.pick(4); got < 0 || got >= 4 {
			t.Fatalf("pick among 4 earlier operations = %d, want from 0 to 3", got)
		}
	}
}
