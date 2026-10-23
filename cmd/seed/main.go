// Command seed ensures the treasury wallet exists and is funded, and creates
// the presenter's account. Idempotent.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/db"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/ledger"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/ids"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/money"
)

// TreasurySeed funds the treasury once: ₦50,000,000.
const TreasurySeed money.Kobo = 5_000_000_000

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()
	pool, err := db.Connect(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		return err
	}
	defer pool.Close()

	// Treasury: a wallet with no user, credited from a one-off "mint" wallet
	// so even the seed is a balanced post.
	var treasury string
	err = pool.QueryRow(ctx, `SELECT id FROM wallets WHERE kind = 'treasury'`).Scan(&treasury)
	if errors.Is(err, pgx.ErrNoRows) {
		treasury = ids.New(ids.Wallet)
		mint := ids.New(ids.Wallet)
		err = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `INSERT INTO wallets (id, kind) VALUES ($1, 'treasury')`, treasury); err != nil {
				return err
			}
			// The mint wallet is the one the overdraw rule exempts: it is the
			// counterparty to creating money, and goes negative exactly once.
			if _, err := tx.Exec(ctx, `INSERT INTO wallets (id, kind) VALUES ($1, 'mint')`, mint); err != nil {
				return err
			}
			return ledger.Post(ctx, tx, mint, treasury, TreasurySeed, ledger.KindTreasurySeed, "seed:treasury")
		})
		if err != nil {
			return fmt.Errorf("seed treasury: %w", err)
		}
		fmt.Println("seed: treasury created and funded")
	} else if err != nil {
		return err
	}

	// Presenter account, created through the same path a sign-up uses.
	hash, _ := bcrypt.GenerateFromPassword([]byte("1234"), bcrypt.DefaultCost)
	userID, walletID := "u_01PRESENTER0000000000000000", "w_01PRESENTER0000000000000000"
	err = db.WithTx(ctx, pool, func(tx pgx.Tx) error {
		// The user row is upserted so a username change in the seed reaches
		// an existing deployment; the wallet and its bonus are created once.
		if _, err := tx.Exec(ctx, `INSERT INTO users (id, username, name, pin_hash) VALUES ($1, 'feranmi', 'Feranmi Adeniji', $2)
			ON CONFLICT (id) DO UPDATE SET username = EXCLUDED.username, name = EXCLUDED.name`, userID, string(hash)); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `INSERT INTO wallets (id, user_id, kind) VALUES ($1, $2, 'user') ON CONFLICT (id) DO NOTHING`, walletID, userID)
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		return ledger.Post(ctx, tx, treasury, walletID, 500_000, ledger.KindSignupBonus, "signup:"+userID)
	})
	if err != nil {
		return fmt.Errorf("seed presenter: %w", err)
	}
	fmt.Println("seed: presenter ensured (feranmi / 1234)")
	return nil
}
