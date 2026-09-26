package scenarios

import (
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// only answers value for key and nothing for every other variable, which is a
// run that sets that one variable alone.
func only(key, value string) func(string) string {
	return func(asked string) string {
		if asked == key {
			return value
		}
		return ""
	}
}

func brl(t *testing.T, amount string) money.Money {
	t.Helper()
	parsed, err := money.Parse(amount, Currency)
	if err != nil {
		t.Fatalf("money.Parse(%q) = %v, want nil", amount, err)
	}
	return parsed
}

// read writes every parameter as text, by the variable it came from, so a case
// states the whole set in one comparison and a failure prints the variable.
func read(params Params) map[string]string {
	return map[string]string{
		InstancesKey:      strconv.Itoa(params.Instances),
		SameBetCopiesKey:  strconv.Itoa(params.SameBetCopies),
		OpeningBalanceKey: params.OpeningBalance.String(),
		RacingBetsKey:     strconv.Itoa(params.RacingBets),
		RacingAmountKey:   params.RacingAmount.String(),
		OtherWalletsKey:   strconv.Itoa(params.OtherWallets),
		PublishersKey:     strconv.Itoa(params.Publishers),
		RestartsKey:       strconv.Itoa(params.Restarts),
		DeadlineKey:       params.Deadline.String(),
	}
}

// A run that sets nothing proves the statement: every variable left empty is
// the default the statement, or the specification where it is silent, fixes.
func TestRead_takesTheDefaultOfEveryVariableLeftEmpty(t *testing.T) {
	t.Parallel()
	params, err := Read(only("", ""))
	if err != nil {
		t.Fatalf("Read of an empty environment = %v, want nil", err)
	}
	want := map[string]string{
		InstancesKey:      "3",
		SameBetCopiesKey:  "50",
		OpeningBalanceKey: "100.00 BRL",
		RacingBetsKey:     "2",
		RacingAmountKey:   "80.00 BRL",
		OtherWalletsKey:   "10",
		PublishersKey:     "2",
		RestartsKey:       "1",
		DeadlineKey:       "2m0s",
	}
	for key, got := range read(params) {
		if got != want[key] {
			t.Errorf("default of %s = %s, want %s", key, got, want[key])
		}
	}
}

// A value that is set is the value read, down to the smallest each variable
// takes: one of a count, one cent of an amount, one nanosecond of a deadline.
func TestRead_takesTheSmallestValueEachVariableAccepts(t *testing.T) {
	t.Parallel()
	set := map[string]string{
		InstancesKey:      "1",
		SameBetCopiesKey:  " 1 ",
		OpeningBalanceKey: "0.01",
		RacingBetsKey:     "1",
		RacingAmountKey:   "0.01",
		OtherWalletsKey:   "1",
		PublishersKey:     "1",
		RestartsKey:       "1",
		DeadlineKey:       "1ns",
	}
	params, err := Read(func(key string) string { return set[key] })
	if err != nil {
		t.Fatalf("Read of the smallest values = %v, want nil", err)
	}
	want := map[string]string{
		InstancesKey:      "1",
		SameBetCopiesKey:  "1",
		OpeningBalanceKey: "0.01 BRL",
		RacingBetsKey:     "1",
		RacingAmountKey:   "0.01 BRL",
		OtherWalletsKey:   "1",
		PublishersKey:     "1",
		RestartsKey:       "1",
		DeadlineKey:       "1ns",
	}
	for key, got := range read(params) {
		if got != want[key] {
			t.Errorf("%s read as %s, want the %s that was set", key, got, want[key])
		}
	}
}

// invalid is every value outside what its variable takes: a count that is not a
// positive integer, an amount the external input of money refuses or that is
// zero, and a duration that is not positive.
func invalid() [][2]string {
	var out [][2]string
	for _, key := range []string{InstancesKey, SameBetCopiesKey, RacingBetsKey, OtherWalletsKey, PublishersKey, RestartsKey} {
		for _, value := range []string{"zero", "0", "-1", "1.5"} {
			out = append(out, [2]string{key, value})
		}
	}
	for _, value := range []string{"0.00", "-5.00", "1e2", "10.001", "NaN", "Infinity", "cem"} {
		out = append(out, [2]string{OpeningBalanceKey, value}, [2]string{RacingAmountKey, value})
	}
	for _, value := range []string{"0s", "-1m", "soon"} {
		out = append(out, [2]string{DeadlineKey, value})
	}
	return out
}

// A value outside what the variable takes fails the whole read, naming that
// variable, and is never replaced by the default.
func TestRead_refusesAnInvalidValueNamingItsVariable(t *testing.T) {
	t.Parallel()
	for _, pair := range invalid() {
		t.Run(pair[0]+"="+pair[1], func(t *testing.T) {
			assertRefusedByName(t, pair[0], pair[1])
		})
	}
}

func assertRefusedByName(t *testing.T, key, value string) {
	t.Helper()
	_, err := Read(only(key, value))
	var refusal InvalidError
	if !errors.As(err, &refusal) {
		t.Fatalf("Read with %s=%q = %v, want an InvalidError", key, value, err)
	}
	if refusal.Key != key || !strings.Contains(err.Error(), key) {
		t.Errorf("refusal = %q naming %s, want it to name %s", err, refusal.Key, key)
	}
}

// The expectation of the racing scenario is computed from the parameters in
// whole cents: the statement, the example of the specification, a balance every
// bet fits exactly, and more room than bets.
func TestFitting_answersHowManyRacingBetsTheOpeningBalanceTakes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		opening string
		amount  string
		bets    int
		want    int64
	}{
		{name: "two of 80.00 over 100.00", opening: "100.00", amount: "80.00", bets: 2, want: 1},
		{name: "five of 30.00 over 100.00", opening: "100.00", amount: "30.00", bets: 5, want: 3},
		{name: "three of 30.00 over exactly 90.00", opening: "90.00", amount: "30.00", bets: 3, want: 3},
		{name: "two of 80.00 over 1000.00", opening: "1000.00", amount: "80.00", bets: 2, want: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := Params{OpeningBalance: brl(t, tc.opening), RacingAmount: brl(t, tc.amount), RacingBets: tc.bets}
			if got := params.Fitting(); got != tc.want {
				t.Errorf("Fitting = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestRemaining_answersTheBalanceAfterEveryBetThatFits(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		opening string
		amount  string
		bets    int
		want    string
	}{
		{name: "two of 80.00 over 100.00", opening: "100.00", amount: "80.00", bets: 2, want: "20.00"},
		{name: "five of 30.00 over 100.00", opening: "100.00", amount: "30.00", bets: 5, want: "10.00"},
		{name: "three of 30.00 over exactly 90.00", opening: "90.00", amount: "30.00", bets: 3, want: "0.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			params := Params{OpeningBalance: brl(t, tc.opening), RacingAmount: brl(t, tc.amount), RacingBets: tc.bets}
			remaining, err := params.Remaining()
			if err != nil {
				t.Fatalf("Remaining = %v, want nil", err)
			}
			if got := remaining.Amount(); got != tc.want {
				t.Errorf("Remaining = %s, want %s", got, tc.want)
			}
		})
	}
}

// Read never builds this pair, and the subtraction still refuses it rather than
// answering a balance in no currency at all.
func TestRemaining_refusesAnAmountInAnotherCurrency(t *testing.T) {
	t.Parallel()
	dollars, err := money.Parse("30.00", "USD")
	if err != nil {
		t.Fatalf("money.Parse of the dollars = %v, want nil", err)
	}
	params := Params{OpeningBalance: brl(t, "100.00"), RacingAmount: dollars, RacingBets: 2}
	if _, err := params.Remaining(); !errors.Is(err, money.ErrCurrencyMismatch) {
		t.Errorf("Remaining over two currencies = %v, want %v", err, money.ErrCurrencyMismatch)
	}
}
