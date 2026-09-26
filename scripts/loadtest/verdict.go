package main

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// tally is what the load counted for one wallet: the movement of the balance
// and the number of operations that moved it.
type tally struct {
	cents int64
	moves int64
}

// tallies counts each operation once, by the first decided answer of its key:
// a replay is the same operation and not a second movement. A LOSS is
// processed without an entry and without moving the version, and a rejection
// moves nothing.
func (l *load) tallies() []tally {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]tally, len(l.wallets))
	for _, op := range l.operations {
		got, known := l.decisions[op.key]
		if !known || got.status != statusProcessed || op.kind == kindLoss {
			continue
		}
		counted := &out[op.wallet]
		counted.moves++
		if op.kind == kindBet {
			counted.cents -= op.cents
			continue
		}
		counted.cents += op.cents
	}
	return out
}

// check reads every wallet of the run back and holds it to what the load
// counted: the balance is the opening plus the WINs less the BETs, the version
// is one plus the movements, and the reconciliation is consistent over the
// movements plus the opening entry. Each difference is a finding naming the
// wallet, the field, what was wanted and what was read.
func (l *load) check(ctx context.Context) []string {
	var findings []string
	for i, counted := range l.tallies() {
		findings = append(findings, l.checkWallet(ctx, l.wallets[i].id, counted)...)
	}
	return findings
}

func (l *load) checkWallet(ctx context.Context, id string, counted tally) []string {
	var stored struct {
		Balance struct {
			Amount string `json:"amount"`
		} `json:"balance"`
		Version int64 `json:"version"`
	}
	code, err := l.call(ctx, l.internal, http.MethodGet, "/wallets/"+id, nil, &stored)
	if finding := readFinding(id, "read", code, err); finding != "" {
		return []string{finding}
	}
	var reconciled struct {
		Consistent     bool  `json:"consistent"`
		CheckedEntries int64 `json:"checkedEntries"`
	}
	code, err = l.call(ctx, l.internal, http.MethodPost, "/wallets/"+id+"/reconciliation", nil, &reconciled)
	if finding := readFinding(id, "reconciliation", code, err); finding != "" {
		return []string{finding}
	}
	var findings []string
	cents, err := centsOf(stored.Balance.Amount)
	if want := openingCents + counted.cents; err != nil || cents != want {
		findings = append(findings, fmt.Sprintf("wallet %s: balance = %s, want %s", id, stored.Balance.Amount, amountOf(want)))
	}
	if want := 1 + counted.moves; stored.Version != want {
		findings = append(findings, fmt.Sprintf("wallet %s: version = %d, want %d", id, stored.Version, want))
	}
	if want := 1 + counted.moves; reconciled.CheckedEntries != want {
		findings = append(findings, fmt.Sprintf("wallet %s: checkedEntries = %d, want %d", id, reconciled.CheckedEntries, want))
	}
	if !reconciled.Consistent {
		findings = append(findings, fmt.Sprintf("wallet %s: consistent = false, want true", id))
	}
	return findings
}

// readFinding answers the finding of a read that did not answer 200, or nothing.
func readFinding(id, read string, code int, err error) string {
	switch {
	case err != nil:
		return fmt.Sprintf("wallet %s: %s: %v", id, read, err)
	case code != http.StatusOK:
		return fmt.Sprintf("wallet %s: %s answered %d, want 200", id, read, code)
	}
	return ""
}

// centsOf reads the decimal string of two places the contract writes. A value
// in any other form is refused rather than rounded.
func centsOf(amount string) (int64, error) {
	whole, fraction, found := strings.Cut(amount, ".")
	if !found || len(fraction) != 2 || strings.HasPrefix(whole, "-") {
		return 0, fmt.Errorf("amount %q is not a decimal of two places", amount)
	}
	return strconv.ParseInt(whole+fraction, 10, 64)
}
