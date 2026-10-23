package httpx

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// Success envelope: { "data": ..., "meta": ... }.
type successBody struct {
	Data any `json:"data"`
	Meta any `json:"meta,omitempty"`
}

// ErrorBody is the error envelope. Clients branch on Reason, display Message,
// and ignore Severity.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail is the inner error object.
type ErrorDetail struct {
	Reason      Reason              `json:"reason"`
	Message     string              `json:"message"`
	Severity    Severity            `json:"severity"`
	FieldErrors map[string][]string `json:"field_errors,omitempty"`
	Data        any                 `json:"data,omitempty"`
	RequestID   string              `json:"request_id,omitempty"`
}

// ctxKeyRequestID is where the request id middleware stores the id.
const ctxKeyRequestID = "request_id"

// RequestID returns the request id set by middleware, or "".
func RequestID(c *gin.Context) string {
	return c.GetString(ctxKeyRequestID)
}

// SetRequestID is called by the request id middleware.
func SetRequestID(c *gin.Context, id string) { c.Set(ctxKeyRequestID, id) }

// OK writes 200 with data.
func OK(c *gin.Context, data any) { c.JSON(http.StatusOK, successBody{Data: data}) }

// OKMeta writes 200 with data and meta (pagination).
func OKMeta(c *gin.Context, data, meta any) {
	c.JSON(http.StatusOK, successBody{Data: data, Meta: meta})
}

// Created writes 201 with data.
func Created(c *gin.Context, data any) { c.JSON(http.StatusCreated, successBody{Data: data}) }

// Accepted writes 202 with optional data.
func Accepted(c *gin.Context, data any) { c.JSON(http.StatusAccepted, successBody{Data: data}) }

// NoContent writes 204 with no body.
func NoContent(c *gin.Context) { c.Status(http.StatusNoContent) }

// Raw writes a previously stored envelope verbatim (idempotency replay).
func Raw(c *gin.Context, status int, body []byte) {
	c.Data(status, "application/json; charset=utf-8", body)
}

// Fail renders any error as the error envelope and aborts the chain. This is
// the one place an error becomes HTTP. Domain errors render as themselves;
// validator errors become validation_error with field detail; anything else
// is a server fault, logged with the request id.
func Fail(c *gin.Context, err error) {
	var de *DomainError
	if errors.As(err, &de) {
		if de.RetryAfter > 0 {
			c.Header("Retry-After", strconv.Itoa(de.RetryAfter))
		}
		if de.Diagnostic != "" {
			slog.Warn("domain error", "reason", de.Reason, "diagnostic", de.Diagnostic, "request_id", RequestID(c))
		}
		c.AbortWithStatusJSON(de.Status, ErrorBody{Error: ErrorDetail{
			Reason:      de.Reason,
			Message:     Message(de.MessageKey),
			Severity:    severityFor(de.Reason),
			FieldErrors: de.FieldErrors,
			Data:        de.Data,
			RequestID:   RequestID(c),
		}})
		return
	}

	var ve validator.ValidationErrors
	if errors.As(err, &ve) {
		Fail(c, Validation(fieldErrors(ve)))
		return
	}

	slog.Error("unhandled error", "err", err, "request_id", RequestID(c))
	c.AbortWithStatusJSON(http.StatusInternalServerError, ErrorBody{Error: ErrorDetail{
		Reason:    ReasonInternal,
		Message:   Message(MsgInternal),
		Severity:  SeverityServerFault,
		RequestID: RequestID(c),
	}})
}

// Bind decodes and validates a JSON body. Malformed JSON and failed
// validation are both client errors (400), never 500.
func Bind(c *gin.Context, dst any) bool {
	if err := c.ShouldBindJSON(dst); err != nil {
		var ve validator.ValidationErrors
		if errors.As(err, &ve) {
			Fail(c, Validation(fieldErrors(ve)))
			return false
		}
		Fail(c, Validation(map[string][]string{"body": {"must be valid JSON matching the schema"}}))
		return false
	}
	return true
}

// fieldErrors turns validator output into the per-field map. All invalid
// fields are returned, not just the first; the frontend renders what it gets.
func fieldErrors(ve validator.ValidationErrors) map[string][]string {
	out := make(map[string][]string, len(ve))
	for _, fe := range ve {
		name := fe.Field()
		out[name] = append(out[name], "failed "+fe.Tag())
	}
	return out
}
