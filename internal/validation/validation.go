// Package validation holds the custom binding rules shared by Gin's
// validator and the one the idempotency path uses for raw bodies.
package validation

import (
	"regexp"

	"github.com/go-playground/validator/v10"
)

// UsernameRe is the shape of a username: lowercase letters, digits and
// underscores, three to twenty-four long. Inputs are lowercased before
// validation so people may type capitals.
var UsernameRe = regexp.MustCompile(`^[a-z0-9_]{3,24}$`)

// Register adds the custom tags to a validator instance.
func Register(v *validator.Validate) {
	_ = v.RegisterValidation("username", func(fl validator.FieldLevel) bool {
		return UsernameRe.MatchString(fl.Field().String())
	})
}

// Username normalises what a person typed into the stored form.
func Username(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c == ' ' || c == '@' {
			continue
		}
		out = append(out, c)
	}
	return string(out)
}
