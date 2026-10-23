// Package money defines the only representation of money in the service:
// integer kobo. Never a float, never naira, never a bare int64 in a domain
// type.
package money

import (
	"encoding/json"
	"fmt"
	"strconv"
)

// Kobo is an amount in the minor unit of the naira. The DB column is BIGINT
// with a _kobo suffix.
type Kobo int64

// maxSafe is the largest integer JSON consumers can hold exactly in a
// double. Above it we emit a string so no digit is lost in transit.
const maxSafe = 1<<53 - 1

// MarshalJSON emits a number when it is exactly representable, else a string.
func (k Kobo) MarshalJSON() ([]byte, error) {
	if k > maxSafe || k < -maxSafe {
		return json.Marshal(strconv.FormatInt(int64(k), 10))
	}
	return json.Marshal(int64(k))
}

// UnmarshalJSON accepts a number or a numeric string.
func (k *Kobo) UnmarshalJSON(b []byte) error {
	var n int64
	if err := json.Unmarshal(b, &n); err == nil {
		*k = Kobo(n)
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("kobo: expected number or numeric string")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return fmt.Errorf("kobo: %w", err)
	}
	*k = Kobo(n)
	return nil
}

// Naira formats for display only. Formatting happens on one side of the seam.
func (k Kobo) Naira() string {
	return fmt.Sprintf("₦%s.%02d", groupThousands(int64(k)/100), int64(k)%100)
}

func groupThousands(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}
