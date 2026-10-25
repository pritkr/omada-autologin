package main

import (
	"fmt"
	"math/rand/v2"
	"strconv"
	"strings"
	"sync"
)

const (
	// maxSuffixDigits bounds the rotated suffix so it always fits in an int
	// and can be zero-padded without overflow.
	maxSuffixDigits = 9

	// suffixBagLimit is the largest range materialised for a full shuffled
	// cycle. Larger ranges use random draws with no immediate repeat so a
	// huge configured range cannot allocate gigabytes.
	suffixBagLimit = 10000
)

// usernameRotator picks a different suffix for each login attempt. Within a
// range it walks a shuffled bag, so every value is used once before any value
// is reused. A daemon restart starts a fresh order.
type usernameRotator struct {
	mu      sync.Mutex
	bag     []int
	pos     int
	bagMin  int
	bagMax  int
	last    int
	hasLast bool
}

func (r *usernameRotator) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bag = nil
	r.pos = 0
	r.bagMin = 0
	r.bagMax = 0
	r.last = 0
	r.hasLast = false
}

// next returns the username for one login attempt. With rotation disabled it
// returns the configured username unchanged.
func (r *usernameRotator) next(cfg *Config) (string, error) {
	rot := cfg.UsernameRotation
	if rot == nil || !rot.Enabled {
		return cfg.Username, nil
	}
	digits := rot.digitsOrDefault()
	if digits < 1 || digits > maxSuffixDigits {
		return "", fmt.Errorf("username_rotation.digits must be between 1 and %d (got %d)", maxSuffixDigits, digits)
	}
	base := strings.TrimSpace(cfg.Username)
	if base != "" && !hasTrailingDigits(base, digits) {
		return "", fmt.Errorf("username_rotation: username %q must end with at least %d digits", base, digits)
	}
	if rot.Min < 1 || rot.Max < rot.Min || rot.Max > pow10(digits)-1 {
		return "", fmt.Errorf("invalid username_rotation range %d-%d for %d digits", rot.Min, rot.Max, digits)
	}
	n := r.draw(rot.Min, rot.Max)
	suffix := fmt.Sprintf("%0*d", digits, n)
	if base == "" {
		return suffix, nil
	}
	return base[:len(base)-digits] + suffix, nil
}

func (r *usernameRotator) draw(min, max int) int {
	r.mu.Lock()
	defer r.mu.Unlock()

	size := max - min + 1
	if size <= 1 {
		r.last = min
		r.hasLast = true
		return min
	}
	if size > suffixBagLimit {
		n := min + rand.IntN(size)
		if r.hasLast && n == r.last {
			n = min + (n-min+1)%size
		}
		r.last = n
		r.hasLast = true
		return n
	}
	if len(r.bag) != size || r.bagMin != min || r.bagMax != max || r.pos >= len(r.bag) {
		bag := make([]int, size)
		for i := range bag {
			bag[i] = min + i
		}
		for i := len(bag) - 1; i > 0; i-- {
			j := rand.IntN(i + 1)
			bag[i], bag[j] = bag[j], bag[i]
		}
		// A new cycle must not begin with the value that ended the old one.
		if r.hasLast && bag[0] == r.last {
			j := 1 + rand.IntN(len(bag)-1)
			bag[0], bag[j] = bag[j], bag[0]
		}
		r.bag = bag
		r.pos = 0
		r.bagMin = min
		r.bagMax = max
	}
	n := r.bag[r.pos]
	r.pos++
	r.last = n
	r.hasLast = true
	return n
}

func (r *UsernameRotation) digitsOrDefault() int {
	if r == nil || r.Digits == 0 {
		return defaultSuffixDigits
	}
	return r.Digits
}

// defaultSuffixMaxFor keeps the product default (001-112) for three digits and
// uses the whole numeric space for other widths.
func defaultSuffixMaxFor(digits int) int {
	if digits == defaultSuffixDigits {
		return defaultSuffixMax
	}
	if digits <= 0 || digits > maxSuffixDigits {
		return 0
	}
	return pow10(digits) - 1
}

func hasTrailingDigits(s string, n int) bool {
	if n <= 0 {
		return true
	}
	if len(s) < n {
		return false
	}
	for _, b := range []byte(s[len(s)-n:]) {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}

func pow10(n int) int {
	p := 1
	for i := 0; i < n; i++ {
		p *= 10
	}
	return p
}

// parseSuffixRange parses a MIN-MAX range such as "001-112". The width of the
// longer side sets the zero-padded suffix width, so "001-112" and "1-112" both
// produce three-digit suffixes 001 through 112.
func parseSuffixRange(raw string) (digits, min, max int, err error) {
	raw = strings.TrimSpace(raw)
	left, right, ok := strings.Cut(raw, "-")
	if !ok {
		return 0, 0, 0, fmt.Errorf("range %q must look like 001-112", raw)
	}
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if !isASCIIDigits(left) || !isASCIIDigits(right) {
		return 0, 0, 0, fmt.Errorf("range %q must contain decimal numbers either side of '-'", raw)
	}
	min, err = strconv.Atoi(left)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("range start %q: %w", left, err)
	}
	max, err = strconv.Atoi(right)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("range end %q: %w", right, err)
	}
	if min < 1 {
		return 0, 0, 0, fmt.Errorf("range %q must start at 001 or higher", raw)
	}
	if max < min {
		return 0, 0, 0, fmt.Errorf("range %q ends before it starts", raw)
	}
	digits = len(left)
	if len(right) > digits {
		digits = len(right)
	}
	if digits > maxSuffixDigits {
		return 0, 0, 0, fmt.Errorf("range %q needs %d digits; at most %d are supported", raw, digits, maxSuffixDigits)
	}
	return digits, min, max, nil
}

func isASCIIDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, b := range []byte(s) {
		if b < '0' || b > '9' {
			return false
		}
	}
	return true
}
