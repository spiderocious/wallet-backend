package app_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/app"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/db"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/env"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/events"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/features/ledger"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/httpx"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/middleware"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/testutil"
)

// Contract tests against a real Postgres. Money paths are tested only here,
// because the overdraw rule lives in a deferred constraint and a mocked
// repository would never exercise it.

var (
	tdb    *testutil.DB
	engine *gin.Engine
	hub    *events.Hub
)

const secret = "wallet-test-secret-wallet-test-secret"

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	d, err := testutil.Start()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	tdb = d
	hub = events.NewHub()
	engine = app.Engine(app.Deps{
		Config: env.Config{Env: "test", JWTSecret: secret, CORSOrigin: "*"},
		Pool:   d.Pool,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Hub:    hub,
	})
	code := m.Run()
	d.Stop()
	os.Exit(code)
}

// ---- helpers ----

// reset truncates and recreates the treasury. The treasury is funded by a
// post from a mint wallet, the one wallet the overdraw rule exempts, exactly
// as cmd/seed does it.
func reset(t *testing.T) {
	t.Helper()
	tdb.Truncate(t)
	ctx := context.Background()
	err := db.WithTx(ctx, tdb.Pool, func(tx pgx.Tx) error {
		for _, q := range []string{
			`INSERT INTO wallets (id, kind) VALUES ('w_TREASURY', 'treasury')`,
			`INSERT INTO wallets (id, kind) VALUES ('w_MINT', 'mint')`,
		} {
			if _, err := tx.Exec(ctx, q); err != nil {
				return err
			}
		}
		return ledger.Post(ctx, tx, "w_MINT", "w_TREASURY", 5_000_000_000, ledger.KindTreasurySeed, "seed:treasury")
	})
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
}

type session struct {
	Token  string `json:"token"`
	User   struct{ ID, Name, Phone string }
	Wallet struct {
		ID          string `json:"id"`
		BalanceKobo int64  `json:"balance_kobo"`
	} `json:"wallet"`
}

type dataEnvelope[T any] struct {
	Data T `json:"data"`
}

type pageEnvelope[T any] struct {
	Data []T `json:"data"`
	Meta struct {
		NextCursor *string `json:"next_cursor"`
		HasMore    bool    `json:"has_more"`
	} `json:"meta"`
}

type errorEnvelope struct {
	Error struct {
		Reason      string              `json:"reason"`
		Message     string              `json:"message"`
		Severity    int                 `json:"severity"`
		FieldErrors map[string][]string `json:"field_errors"`
		Data        json.RawMessage     `json:"data"`
		RequestID   string              `json:"request_id"`
	} `json:"error"`
}

type transferWire struct {
	ID              string     `json:"id"`
	FromUserID      string     `json:"from_user_id"`
	FromName        string     `json:"from_name"`
	ToUserID        string     `json:"to_user_id"`
	ToName          string     `json:"to_name"`
	AmountKobo      int64      `json:"amount_kobo"`
	Status          string     `json:"status"`
	IdempotencyKey  *string    `json:"idempotency_key"`
	ClientCreatedAt *time.Time `json:"client_created_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type createWire struct {
	Transfer    transferWire `json:"transfer"`
	BalanceKobo int64        `json:"balance_kobo"`
}

func call(t *testing.T, token, method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var r io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		r = bytes.NewReader(b)
	}
	hr := httptest.NewRequest(method, path, r)
	hr.Header.Set("Content-Type", "application/json")
	if token != "" {
		hr.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		hr.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, hr)
	return w
}

func decode[T any](t *testing.T, w *httptest.ResponseRecorder) T {
	t.Helper()
	var out T
	dec := json.NewDecoder(bytes.NewReader(w.Body.Bytes()))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("decode %T: %v\nbody: %s", out, err, w.Body.String())
	}
	return out
}

func signup(t *testing.T, phone, name string) session {
	t.Helper()
	w := call(t, "", "POST", "/api/v1/auth/signup", map[string]string{"phone": phone, "name": name, "pin": "1234"}, nil)
	if w.Code != 201 {
		t.Fatalf("signup %d: %s", w.Code, w.Body)
	}
	return decode[dataEnvelope[session]](t, w).Data
}

func balance(t *testing.T, token string) int64 {
	t.Helper()
	w := call(t, token, "GET", "/api/v1/wallet", nil, nil)
	if w.Code != 200 {
		t.Fatalf("wallet %d: %s", w.Code, w.Body)
	}
	return decode[dataEnvelope[struct {
		ID          string `json:"id"`
		BalanceKobo int64  `json:"balance_kobo"`
	}]](t, w).Data.BalanceKobo
}

func send(t *testing.T, token, toPhone string, kobo int64, key string) *httptest.ResponseRecorder {
	t.Helper()
	h := map[string]string{}
	if key != "" {
		h["Idempotency-Key"] = key
	}
	return call(t, token, "POST", "/api/v1/transfers", map[string]any{"to_phone": toPhone, "amount_kobo": kobo}, h)
}

// ---- tests ----

func TestSignupCreditsFiveThousandThroughTheLedger(t *testing.T) {
	reset(t)
	s := signup(t, "08030000001", "Ayomide Bello")
	if s.Wallet.BalanceKobo != 500_000 {
		t.Fatalf("balance %d", s.Wallet.BalanceKobo)
	}
	var entries int
	var credit int64
	_ = tdb.Pool.QueryRow(context.Background(),
		`SELECT count(*), COALESCE(SUM(CASE WHEN type='credit' THEN amount_kobo END),0) FROM ledger_entries WHERE reference = $1`, "signup:"+s.User.ID).
		Scan(&entries, &credit)
	if entries != 2 || credit != 500_000 {
		t.Fatalf("ledger rows %d credit %d", entries, credit)
	}

	t.Run("same phone again is already_exists", func(t *testing.T) {
		w := call(t, "", "POST", "/api/v1/auth/signup", map[string]string{"phone": "08030000001", "name": "Again", "pin": "1234"}, nil)
		if w.Code != 409 || decode[errorEnvelope](t, w).Error.Reason != string(httpx.ReasonAlreadyExists) {
			t.Fatalf("%d %s", w.Code, w.Body)
		}
	})

	t.Run("no balance column was written by application code", func(t *testing.T) {
		// The cache must equal the ledger for every wallet, including the treasury.
		w := call(t, s.Token, "GET", "/api/v1/ops/reconcile", nil, nil)
		out := decode[dataEnvelope[struct {
			Clean  bool              `json:"clean"`
			Drifts []json.RawMessage `json:"drifts"`
		}]](t, w).Data
		if !out.Clean {
			t.Fatalf("drift: %s", w.Body)
		}
	})
}

func TestTransferPostsTwoBalancedRows(t *testing.T) {
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	b := signup(t, "08030000002", "Chukwuemeka Obi")

	w := send(t, a.Token, "08030000002", 150_000, "k1")
	if w.Code != 201 {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	out := decode[dataEnvelope[createWire]](t, w).Data
	if out.BalanceKobo != 350_000 || out.Transfer.Status != "posted" || out.Transfer.ToName != "Chukwuemeka Obi" {
		t.Fatalf("%+v", out)
	}
	if balance(t, a.Token) != 350_000 || balance(t, b.Token) != 650_000 {
		t.Fatalf("balances %d %d", balance(t, a.Token), balance(t, b.Token))
	}
	var rows int
	_ = tdb.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE reference = $1`, out.Transfer.ID).Scan(&rows)
	if rows != 2 {
		t.Fatalf("ledger rows %d", rows)
	}
}

func TestOverdrawIsRefusedAtCommitAndRecorded(t *testing.T) {
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	signup(t, "08030000002", "Chukwuemeka Obi")

	w := send(t, a.Token, "08030000002", 500_001, "k-over")
	if w.Code != 422 {
		t.Fatalf("%d: %s", w.Code, w.Body)
	}
	e := decode[errorEnvelope](t, w)
	if e.Error.Reason != string(httpx.ReasonInsufficientBalance) || e.Error.Message == "" {
		t.Fatalf("%+v", e.Error)
	}
	var cur struct {
		BalanceKobo int64 `json:"balance_kobo"`
	}
	_ = json.Unmarshal(e.Error.Data, &cur)
	if cur.BalanceKobo != 500_000 {
		t.Fatalf("real balance in error data: %d", cur.BalanceKobo)
	}
	if balance(t, a.Token) != 500_000 {
		t.Fatalf("balance changed on a rejected transfer")
	}
	var status string
	_ = tdb.Pool.QueryRow(context.Background(), `SELECT status FROM transfers WHERE idempotency_key = 'k-over'`).Scan(&status)
	if status != "rejected" {
		t.Fatalf("rejected transfer not recorded: %q", status)
	}
	var ledgerRows int
	_ = tdb.Pool.QueryRow(context.Background(), `SELECT count(*) FROM ledger_entries WHERE kind = 'transfer'`).Scan(&ledgerRows)
	if ledgerRows != 0 {
		t.Fatalf("ledger rows leaked from a rolled-back post: %d", ledgerRows)
	}
}

func TestTwoDevicesOneAccount(t *testing.T) {
	// The slide's example: both devices saw ₦5,000 and each queued ₦3,000.
	// Arrival order decides; the server accepts the first and refuses the
	// second, so the ledger never goes negative.
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	signup(t, "08030000002", "Chukwuemeka Obi")
	signup(t, "08030000003", "Nneka Okafor")

	first := send(t, a.Token, "08030000002", 300_000, "phone-a")
	second := send(t, a.Token, "08030000003", 300_000, "tablet-b")
	if first.Code != 201 || second.Code != 422 {
		t.Fatalf("first %d second %d: %s", first.Code, second.Code, second.Body)
	}
	if balance(t, a.Token) != 200_000 {
		t.Fatalf("balance %d", balance(t, a.Token))
	}
}

func TestIdempotentReplayAndMismatch(t *testing.T) {
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	signup(t, "08030000002", "Chukwuemeka Obi")

	first := send(t, a.Token, "08030000002", 50_000, "k-replay")
	second := send(t, a.Token, "08030000002", 50_000, "k-replay")
	if first.Code != 201 || second.Code != 201 || first.Body.String() != second.Body.String() {
		t.Fatalf("replay: %d %d\n%s\n%s", first.Code, second.Code, first.Body, second.Body)
	}
	if balance(t, a.Token) != 450_000 {
		t.Fatalf("replay charged twice: %d", balance(t, a.Token))
	}

	w := send(t, a.Token, "08030000002", 60_000, "k-replay")
	if w.Code != 422 || decode[errorEnvelope](t, w).Error.Reason != string(httpx.ReasonIdempotencyMismatch) {
		t.Fatalf("mismatch: %d %s", w.Code, w.Body)
	}

	t.Run("two taps are two intents, two keys, two transfers", func(t *testing.T) {
		x := send(t, a.Token, "08030000002", 10_000, "tap-1")
		y := send(t, a.Token, "08030000002", 10_000, "tap-2")
		if x.Code != 201 || y.Code != 201 {
			t.Fatalf("%d %d", x.Code, y.Code)
		}
		if balance(t, a.Token) != 430_000 {
			t.Fatalf("balance %d", balance(t, a.Token))
		}
	})
}

func TestSelfTransferAndUnknownRecipient(t *testing.T) {
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	w := send(t, a.Token, "08030000001", 1_000, "")
	if w.Code != 422 || decode[errorEnvelope](t, w).Error.Reason != string(httpx.ReasonSelfTransfer) {
		t.Fatalf("self: %d %s", w.Code, w.Body)
	}
	w = send(t, a.Token, "08039999999", 1_000, "")
	if w.Code != 404 || decode[errorEnvelope](t, w).Error.Reason != string(httpx.ReasonRecipientNotFound) {
		t.Fatalf("unknown: %d %s", w.Code, w.Body)
	}
	w = call(t, a.Token, "POST", "/api/v1/transfers", map[string]any{"to_phone": "08030000002", "amount_kobo": 0}, nil)
	if w.Code != 400 || decode[errorEnvelope](t, w).Error.FieldErrors["AmountKobo"] == nil {
		t.Fatalf("zero amount: %d %s", w.Code, w.Body)
	}
}

func TestRecipientHearsAboutItOverSSE(t *testing.T) {
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	b := signup(t, "08030000002", "Chukwuemeka Obi")

	ch, unsubscribe := hub.Subscribe(b.User.ID)
	defer unsubscribe()

	if w := send(t, a.Token, "08030000002", 25_000, "k-sse"); w.Code != 201 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	e, ok := events.WaitFor(ctx, ch, "transfer.received")
	if !ok {
		t.Fatal("no transfer.received event")
	}
	b2, _ := json.Marshal(e.Data)
	var got struct {
		Transfer    transferWire `json:"transfer"`
		BalanceKobo int64        `json:"balance_kobo"`
	}
	if err := json.Unmarshal(b2, &got); err != nil || got.Transfer.AmountKobo != 25_000 || got.BalanceKobo != 525_000 {
		t.Fatalf("event %s err %v", b2, err)
	}

	t.Run("a rejected transfer is never announced", func(t *testing.T) {
		if w := send(t, a.Token, "08030000002", 9_999_999, "k-sse-over"); w.Code != 422 {
			t.Fatalf("%d", w.Code)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
		defer cancel()
		if _, ok := events.WaitFor(ctx, ch, "transfer.received"); ok {
			t.Fatal("rejected transfer was announced")
		}
	})

	t.Run("the stream handler writes the event frame", func(t *testing.T) {
		hr := httptest.NewRequest("GET", "/api/v1/events", nil)
		hr.Header.Set("Authorization", "Bearer "+b.Token)
		ctx, cancel := context.WithCancel(context.Background())
		hr = hr.WithContext(ctx)
		w := httptest.NewRecorder()
		done := make(chan struct{})
		go func() { engine.ServeHTTP(w, hr); close(done) }()
		deadline := time.Now().Add(2 * time.Second)
		for hub.Subscribers(b.User.ID) < 2 && time.Now().Before(deadline) {
			time.Sleep(10 * time.Millisecond)
		}
		send(t, a.Token, "08030000002", 1_000, "k-frame")
		time.Sleep(100 * time.Millisecond)
		cancel()
		<-done
		if !strings.Contains(w.Body.String(), "event: transfer.received") {
			t.Fatalf("stream body: %q", w.Body.String())
		}
	})
}

func TestTransactionsPaginate(t *testing.T) {
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	signup(t, "08030000002", "Chukwuemeka Obi")
	for i := 0; i < 5; i++ {
		send(t, a.Token, "08030000002", 1_000, fmt.Sprintf("p-%d", i))
	}
	p1 := decode[pageEnvelope[transferWire]](t, call(t, a.Token, "GET", "/api/v1/transactions?limit=2", nil, nil))
	if len(p1.Data) != 2 || !p1.Meta.HasMore || p1.Meta.NextCursor == nil {
		t.Fatalf("%+v", p1.Meta)
	}
	p2 := decode[pageEnvelope[transferWire]](t, call(t, a.Token, "GET", "/api/v1/transactions?limit=2&cursor="+*p1.Meta.NextCursor, nil, nil))
	p3 := decode[pageEnvelope[transferWire]](t, call(t, a.Token, "GET", "/api/v1/transactions?limit=2&cursor="+*p2.Meta.NextCursor, nil, nil))
	if len(p3.Data) != 1 || p3.Meta.HasMore {
		t.Fatalf("%+v", p3.Meta)
	}
}

func TestStageView(t *testing.T) {
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	me := signup(t, "08030000000", "Feranmi Adeniji")
	send(t, a.Token, "08030000000", 50_000, "stage-1")
	send(t, a.Token, "08030000000", 50_000, "stage-2")
	w := call(t, me.Token, "GET", "/api/v1/ops/stage", nil, nil)
	st := decode[dataEnvelope[struct {
		Name          string `json:"name"`
		BalanceKobo   int64  `json:"balance_kobo"`
		ReceivedToday int    `json:"received_today"`
		DistinctKeys  int    `json:"distinct_keys"`
		Recent        []struct {
			ID         string `json:"id"`
			FromName   string `json:"from_name"`
			AmountKobo int64  `json:"amount_kobo"`
			KeyTail    string `json:"key_tail"`
			CreatedAt  string `json:"created_at"`
		} `json:"recent"`
	}]](t, w).Data
	if st.BalanceKobo != 600_000 || st.ReceivedToday != 2 || st.DistinctKeys != 2 || len(st.Recent) != 2 || st.Recent[0].KeyTail != "tage-2" {
		t.Fatalf("%+v", st)
	}
}

func TestLedgerIsAppendOnly(t *testing.T) {
	reset(t)
	_, err := tdb.Pool.Exec(context.Background(), `UPDATE ledger_entries SET amount_kobo = 1`)
	if err == nil || !strings.Contains(err.Error(), "append-only") {
		t.Fatalf("update allowed: %v", err)
	}
	_, err = tdb.Pool.Exec(context.Background(), `DELETE FROM ledger_entries`)
	if err == nil {
		t.Fatal("delete allowed")
	}
}

func TestTokenScopesWallet(t *testing.T) {
	reset(t)
	signup(t, "08030000001", "Ayomide Bello")
	tok, _ := middleware.SignToken(secret, "u_NOBODY", time.Hour)
	w := call(t, tok, "GET", "/api/v1/wallet", nil, nil)
	if w.Code != 404 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
}

// ---- telemetry ----

func TestTelemetry(t *testing.T) {
	reset(t)
	a := signup(t, "08030000001", "Ayomide Bello")
	w := call(t, a.Token, "POST", "/api/v1/telemetry", map[string]any{"events": []map[string]any{
		{"kind": "queue.drained", "payload": map[string]int{"sent": 2}, "occurred_at": time.Now().UTC()},
		{"kind": "app.start", "occurred_at": time.Now().UTC()},
	}}, nil)
	if w.Code != 202 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	out := decode[dataEnvelope[struct {
		Accepted int `json:"accepted"`
		Skipped  int `json:"skipped"`
	}]](t, w).Data
	if out.Accepted != 2 || out.Skipped != 0 {
		t.Fatalf("%+v", out)
	}
	if w := call(t, "", "POST", "/api/v1/telemetry", map[string]any{"events": []any{}}, nil); w.Code != 401 {
		t.Fatalf("anonymous telemetry: %d", w.Code)
	}
}
