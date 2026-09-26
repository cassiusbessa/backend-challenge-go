package scenarios

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/junglegaming/backend-challenge-go/internal/domain/money"
)

// The variables every quantity of the scenarios is read from. The name is what a
// refusal carries, so whoever set the value reads which one to fix.
const (
	InstancesKey      = "SCENARIO_INSTANCES"
	SameBetCopiesKey  = "SCENARIO_SAME_BET_COPIES"
	OpeningBalanceKey = "SCENARIO_OPENING_BALANCE"
	RacingBetsKey     = "SCENARIO_RACING_BETS"
	RacingAmountKey   = "SCENARIO_RACING_AMOUNT"
	OtherWalletsKey   = "SCENARIO_OTHER_WALLETS"
	PublishersKey     = "SCENARIO_PUBLISHERS"
	RestartsKey       = "SCENARIO_RESTARTS"
	DeadlineKey       = "SCENARIO_DEADLINE"
)

// Currency is the one every scenario settles in, which is the one the statement
// writes its amounts in.
const Currency = "BRL"

// Params is every quantity the scenarios take. The zero value is not a set of
// parameters: Read is the only constructor, and it fills every field.
type Params struct {
	Instances      int
	SameBetCopies  int
	OpeningBalance money.Money
	RacingBets     int
	RacingAmount   money.Money
	OtherWallets   int
	Publishers     int
	Restarts       int
	Deadline       time.Duration
}

// InvalidError is a variable set to a value it does not take. It names the
// variable and what the variable takes.
type InvalidError struct {
	Key  string
	Want string
}

func (e InvalidError) Error() string {
	return "scenarios: " + e.Key + " is not " + e.Want
}

// Read answers the parameters, each from its variable through lookup, and the
// default for a variable that is unset or empty. A value that is set and invalid
// is refused by name and never replaced by the default: a scenario that ran on
// the default when asked for something else would pass without proving what was
// asked. So is a set of values under which a scenario could not fail.
func Read(lookup func(string) string) (Params, error) {
	r := reader{lookup: lookup}
	// The statement fixes every default but three: it names no count of other
	// wallets, no count of restarts and no deadline, so those are the ones the
	// specification of the scenarios fixes. A floor of two is where one would be
	// no other instance, no replay, no race and no dispute.
	params := Params{
		Instances:      r.count(InstancesKey, 3, 2),
		SameBetCopies:  r.count(SameBetCopiesKey, 50, 2),
		OpeningBalance: r.amount(OpeningBalanceKey, "100.00"),
		RacingBets:     r.count(RacingBetsKey, 2, 2),
		RacingAmount:   r.amount(RacingAmountKey, "80.00"),
		OtherWallets:   r.count(OtherWalletsKey, 10, 1),
		Publishers:     r.count(PublishersKey, 2, 2),
		Restarts:       r.count(RestartsKey, 1, 1),
		Deadline:       r.duration(DeadlineKey, 2*time.Minute),
	}
	if r.refusal != nil {
		return Params{}, r.refusal
	}
	if refusal := params.contested(); refusal != nil {
		return Params{}, refusal
	}
	return params, nil
}

// contested refuses values each valid on its own that together leave a scenario
// nothing to decide: fewer copies of the same bet than instances, which leaves an
// instance with no arrival to take, and a race in which no bet fits or every bet
// does, which settles the same whatever order the lock gives the bets.
func (p Params) contested() error {
	if p.SameBetCopies < p.Instances {
		return InvalidError{Key: SameBetCopiesKey, Want: "a count of at least " + InstancesKey}
	}
	if fits := p.Fitting(); fits < 1 || fits >= int64(p.RacingBets) {
		return InvalidError{Key: RacingAmountKey, Want: "an amount " + OpeningBalanceKey + " takes at least once and fewer times than " + RacingBetsKey}
	}
	return nil
}

// Fitting answers how many of the racing bets fit the opening balance: the lesser
// of the number of bets and the balance over the amount, in whole cents.
func (p Params) Fitting() int64 {
	return min(int64(p.RacingBets), p.OpeningBalance.Cents()/p.RacingAmount.Cents())
}

// Remaining answers the balance the racing wallet ends at: the opening less the
// amount once per bet that fits, through the subtraction that refuses an
// overflow and a second currency.
func (p Params) Remaining() (money.Money, error) {
	balance := p.OpeningBalance
	for range p.Fitting() {
		var err error
		if balance, err = balance.Sub(p.RacingAmount); err != nil {
			return money.Money{}, fmt.Errorf("subtract the racing amount: %w", err)
		}
	}
	return balance, nil
}

// reader reads one variable after another and keeps the first refusal. The
// variables after it are not read, and the refusal is what Read answers.
type reader struct {
	lookup  func(string) string
	refusal error
}

// raw answers the value of the variable, or the fallback for one that is unset
// or empty, and reports whether there is still something to read.
func (r *reader) raw(key, fallback string) (string, bool) {
	if r.refusal != nil {
		return "", false
	}
	if value := strings.TrimSpace(r.lookup(key)); value != "" {
		return value, true
	}
	return fallback, true
}

func (r *reader) refuse(key, want string) {
	r.refusal = InvalidError{Key: key, Want: want}
}

func (r *reader) count(key string, fallback, least int) int {
	value, reading := r.raw(key, strconv.Itoa(fallback))
	if !reading {
		return 0
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < least {
		r.refuse(key, "an integer of at least "+strconv.Itoa(least))
		return 0
	}
	return parsed
}

// amount takes the rules of the external input of money — no exponent, no third
// place, no sign — and asks for more than zero on top of them: every amount of
// a scenario is one a bet moves.
func (r *reader) amount(key, fallback string) money.Money {
	value, reading := r.raw(key, fallback)
	if !reading {
		return money.Money{}
	}
	parsed, err := money.Parse(value, Currency)
	if err != nil || !parsed.IsPositive() {
		r.refuse(key, "an amount above zero")
		return money.Money{}
	}
	return parsed
}

func (r *reader) duration(key string, fallback time.Duration) time.Duration {
	value, reading := r.raw(key, fallback.String())
	if !reading {
		return 0
	}
	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		r.refuse(key, "a positive duration")
		return 0
	}
	return parsed
}
