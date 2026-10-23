// Package httpx owns the HTTP envelope: how success and failure are written,
// the stable error identities clients branch on, and the message catalog.
//
// An error response does three jobs and each has its own field:
//
//	reason   - stable snake_case identity; the contract clients switch on
//	message  - resolved human text from the catalog; what a person reads
//	severity - a coarse band for dashboards; never used for branching
package httpx

import (
	"fmt"
	"net/http"
)

// Reason is a stable error identity. Renaming one is a breaking change.
type Reason string

// Every identity the service can return.
const (
	ReasonValidation          Reason = "validation_error"
	ReasonInvalidCredentials  Reason = "invalid_credentials"
	ReasonTokenInvalid        Reason = "token_invalid"
	ReasonNotFound            Reason = "not_found"
	ReasonAlreadyExists       Reason = "already_exists"
	ReasonInsufficientBalance Reason = "insufficient_balance"
	ReasonRecipientNotFound   Reason = "recipient_not_found"
	ReasonSelfTransfer        Reason = "self_transfer"
	ReasonIdempotencyMismatch Reason = "idempotency_mismatch"
	ReasonRateLimited         Reason = "rate_limited"
	ReasonInternal            Reason = "internal_error"
)

// Severity answers "should this page someone?", not "what went wrong?".
type Severity int

// Bands, roughly ordered by who is at fault.
const (
	SeverityBodyValidation       Severity = 1
	SeveritySuspiciousValidation Severity = 2
	SeverityAuth                 Severity = 3
	SeverityForbidden            Severity = 4
	SeverityNotFound             Severity = 5
	SeverityConflict             Severity = 6
	SeverityBusinessRule         Severity = 7
	SeverityRateLimited          Severity = 8
	SeverityUpstream             Severity = 9
	SeverityServerFault          Severity = 10
)

func severityFor(r Reason) Severity {
	switch r {
	case ReasonValidation:
		return SeverityBodyValidation
	case ReasonIdempotencyMismatch, ReasonSelfTransfer:
		// A well-behaved client never sends these; they mean a client bug or
		// tampering, which is worth a look on a dashboard.
		return SeveritySuspiciousValidation
	case ReasonInvalidCredentials, ReasonTokenInvalid:
		return SeverityAuth
	case ReasonNotFound, ReasonRecipientNotFound:
		return SeverityNotFound
	case ReasonAlreadyExists:
		return SeverityConflict
	case ReasonInsufficientBalance:
		return SeverityBusinessRule
	case ReasonRateLimited:
		return SeverityRateLimited
	default:
		return SeverityServerFault
	}
}

// DomainError is a domain rejection returned as a value from a service.
type DomainError struct {
	Reason      Reason
	MessageKey  MessageKey
	Status      int
	FieldErrors map[string][]string
	Data        any
	RetryAfter  int
	Diagnostic  string
}

func (e *DomainError) Error() string {
	if e.Diagnostic != "" {
		return fmt.Sprintf("%s: %s", e.Reason, e.Diagnostic)
	}
	return string(e.Reason)
}

// Domain builds a DomainError.
func Domain(r Reason, key MessageKey, status int) *DomainError {
	return &DomainError{Reason: r, MessageKey: key, Status: status}
}

// WithData attaches recovery data.
func (e *DomainError) WithData(d any) *DomainError { e.Data = d; return e }

// WithDiagnostic attaches operator-facing detail.
func (e *DomainError) WithDiagnostic(d string) *DomainError { e.Diagnostic = d; return e }

// NotFound is the standard not_found error.
func NotFound(key MessageKey) *DomainError {
	return Domain(ReasonNotFound, key, http.StatusNotFound)
}

// Validation is the standard validation_error with per-field detail.
func Validation(fields map[string][]string) *DomainError {
	e := Domain(ReasonValidation, MsgValidation, http.StatusBadRequest)
	e.FieldErrors = fields
	return e
}
