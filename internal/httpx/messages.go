package httpx

// MessageKey names a human-readable message in the catalog.
type MessageKey string

// Keys, sliced by feature.
const (
	MsgValidation          MessageKey = "common.validation"
	MsgInternal            MessageKey = "common.internal"
	MsgRateLimited         MessageKey = "common.rate_limited"
	MsgIdempotencyMismatch MessageKey = "common.idempotency_mismatch"
	MsgTokenInvalid        MessageKey = "auth.token_invalid"
	MsgInvalidCredentials  MessageKey = "auth.invalid_credentials"
	MsgUsernameTaken       MessageKey = "auth.username_taken"
	MsgInsufficientBalance MessageKey = "transfers.insufficient_balance"
	MsgRecipientNotFound   MessageKey = "transfers.recipient_not_found"
	MsgSelfTransfer        MessageKey = "transfers.self_transfer"
	MsgWalletNotFound      MessageKey = "wallet.not_found"
)

var catalog = map[MessageKey]string{
	MsgValidation:          "Some fields need attention.",
	MsgInternal:            "Something went wrong on our side. Please try again.",
	MsgRateLimited:         "Too many requests. Please wait a moment.",
	MsgIdempotencyMismatch: "This request was already made with different details.",
	MsgTokenInvalid:        "Your session has expired. Please sign in again.",
	MsgInvalidCredentials:  "Username or PIN is incorrect.",
	MsgUsernameTaken:       "That username is taken.",
	MsgInsufficientBalance: "Your balance is too low for this transfer.",
	MsgRecipientNotFound:   "We could not find anyone with that username.",
	MsgSelfTransfer:        "You cannot send money to yourself.",
	MsgWalletNotFound:      "Wallet not found.",
}

// Message resolves a key to display text.
func Message(k MessageKey) string {
	if m, ok := catalog[k]; ok {
		return m
	}
	return string(k)
}
