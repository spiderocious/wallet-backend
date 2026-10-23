package transfers

import (
	"encoding/json"
	"sync"

	"github.com/go-playground/validator/v10"

	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/httpx"
	"github.com/spiderocious/devfest-akure-2026/wallet/backend/internal/validation"
)

// validate is the same engine Gin uses, for bodies the idempotency
// middleware already read.
var validate = sync.OnceValue(func() *validator.Validate {
	v := validator.New(validator.WithRequiredStructEnabled())
	v.SetTagName("binding")
	validation.Register(v)
	return v
})

func bindRaw(raw []byte, dst any) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return httpx.Validation(map[string][]string{"body": {"must be valid JSON"}})
	}
	return validate().Struct(dst)
}
