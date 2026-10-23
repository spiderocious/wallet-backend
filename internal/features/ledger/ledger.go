// Package ledger is the only code that writes money. A Post is two balanced
// rows in one transaction; the balance cache is maintained by a trigger and
// the overdraw rule is a deferred constraint trigger checked at COMMIT.
package ledger

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/db"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/ids"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/money"
)

// Kind labels why money moved.
type Kind string

// Kinds.
const (
	KindTransfer     Kind = "transfer"
	KindSignupBonus  Kind = "signup_bonus"
	KindTreasurySeed Kind = "treasury_seed"
)

// ErrOverdraw is returned by Post when COMMIT violates balance_non_negative.
// Callers map it to the insufficient_balance domain error.
var ErrOverdraw = errors.New("ledger: overdraw")

// Entry is a ledger row on the wire.
type Entry struct {
	ID         string     `json:"id"`
	WalletID   string     `json:"wallet_id"`
	Type       string     `json:"type"`
	AmountKobo money.Kobo `json:"amount_kobo"`
	Reference  string     `json:"reference"`
	Kind       Kind       `json:"kind"`
	CreatedAt  time.Time  `json:"created_at"`
}

// Post moves amount from one wallet to another as a balanced pair. It
// asserts the pair balances before writing, so a bug in a caller cannot
// unbalance the ledger. It runs inside tx; the caller owns the commit and
// must translate a 23514 on balance_non_negative into ErrOverdraw.
func Post(ctx context.Context, tx pgx.Tx, from, to string, amount money.Kobo, kind Kind, reference string) error {
	if amount <= 0 {
		return fmt.Errorf("ledger: amount must be positive")
	}
	if from == to {
		return fmt.Errorf("ledger: from and to must differ")
	}
	// The overdraw rule is a deferred constraint trigger evaluated at
	// COMMIT. Stating it explicitly in every posting transaction keeps the
	// behaviour independent of session settings a pooled connection carries.
	if _, err := tx.Exec(ctx, `SET CONSTRAINTS balance_non_negative DEFERRED`); err != nil {
		return err
	}
	debit, credit := amount, amount
	if debit != credit {
		return fmt.Errorf("ledger: unbalanced post")
	}
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO ledger_entries (id, wallet_id, type, amount_kobo, reference, kind) VALUES ($1, $2, 'debit', $3, $4, $5)`,
		ids.New(ids.Ledger), from, int64(debit), reference, string(kind))
	batch.Queue(`INSERT INTO ledger_entries (id, wallet_id, type, amount_kobo, reference, kind) VALUES ($1, $2, 'credit', $3, $4, $5)`,
		ids.New(ids.Ledger), to, int64(credit), reference, string(kind))
	res := tx.SendBatch(ctx, batch)
	for i := 0; i < 2; i++ {
		if _, err := res.Exec(); err != nil {
			_ = res.Close()
			return err
		}
	}
	return res.Close()
}

// MapCommit turns a commit error into ErrOverdraw when it is the balance
// constraint, else returns it unchanged.
func MapCommit(err error) error {
	if err == nil {
		return nil
	}
	if db.IsCheckViolation(err, "balance_non_negative") {
		return ErrOverdraw
	}
	return err
}

// Repo reads balances and entries.
type Repo struct{ Pool *pgxpool.Pool }

// Balance reads the cached balance. A wallet with no entries is zero.
func (r Repo) Balance(ctx context.Context, walletID string) (money.Kobo, error) {
	var b int64
	err := r.Pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance_kobo FROM wallet_balances WHERE wallet_id = $1), 0)`, walletID).Scan(&b)
	return money.Kobo(b), err
}

// Drift is one wallet whose cache disagrees with the ledger.
type Drift struct {
	WalletID string     `json:"wallet_id"`
	Cached   money.Kobo `json:"cached_kobo"`
	Computed money.Kobo `json:"computed_kobo"`
}

// Reconcile compares SUM(ledger_entries) to wallet_balances for every wallet
// and returns the rows that disagree. It never corrects; drift is an alert.
func (r Repo) Reconcile(ctx context.Context) ([]Drift, error) {
	rows, err := r.Pool.Query(ctx, `
		SELECT wb.wallet_id, wb.balance_kobo,
		       COALESCE(SUM(CASE WHEN le.type = 'credit' THEN le.amount_kobo ELSE -le.amount_kobo END), 0)
		FROM wallet_balances wb
		LEFT JOIN ledger_entries le ON le.wallet_id = wb.wallet_id
		GROUP BY wb.wallet_id, wb.balance_kobo
		HAVING wb.balance_kobo <> COALESCE(SUM(CASE WHEN le.type = 'credit' THEN le.amount_kobo ELSE -le.amount_kobo END), 0)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Drift{}
	for rows.Next() {
		var d Drift
		var c, m int64
		if err := rows.Scan(&d.WalletID, &c, &m); err != nil {
			return nil, err
		}
		d.Cached, d.Computed = money.Kobo(c), money.Kobo(m)
		out = append(out, d)
	}
	return out, rows.Err()
}
