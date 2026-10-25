package main

import (
	"strconv"
	"strings"
	"testing"
)

func TestDefaultConfigRotates001To112(t *testing.T) {
	cfg := defaultConfig()
	r := cfg.UsernameRotation
	if r == nil {
		t.Fatal("new configs must enable username rotation")
	}
	if !r.Enabled || r.Digits != 3 || r.Min != 1 || r.Max != 112 {
		t.Fatalf("unexpected default rotation: %+v", *r)
	}
}

func TestParseSuffixRange(t *testing.T) {
	cases := []struct {
		raw              string
		digits, min, max int
		ok               bool
	}{
		{"001-112", 3, 1, 112, true},
		{"1-112", 3, 1, 112, true},
		{"020-112", 3, 20, 112, true},
		{"01-09", 2, 1, 9, true},
		{"0001-0112", 4, 1, 112, true},
		{"001-1000", 4, 1, 1000, true},
		{"0-112", 0, 0, 0, false},
		{"112-001", 0, 0, 0, false},
		{"abc", 0, 0, 0, false},
		{"001", 0, 0, 0, false},
		{"001-1000000000", 0, 0, 0, false},
		{"001-112-2", 0, 0, 0, false},
	}
	for _, tc := range cases {
		digits, min, max, err := parseSuffixRange(tc.raw)
		if tc.ok {
			if err != nil {
				t.Errorf("%s: unexpected error: %v", tc.raw, err)
				continue
			}
			if digits != tc.digits || min != tc.min || max != tc.max {
				t.Errorf("%s: got %d/%d/%d, want %d/%d/%d",
					tc.raw, digits, min, max, tc.digits, tc.min, tc.max)
			}
			continue
		}
		if err == nil {
			t.Errorf("%s: expected an error", tc.raw)
		}
	}
}

// TestCSEUsernameRange is the concrete registration-number format: keep the
// 24105157 prefix and rotate the final three digits from 001 through 112.
func TestCSEUsernameRange(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = "24105157001"
	cfg.Password = "test-pass"
	cfg.applyDefaults()

	r := cfg.UsernameRotation
	if r == nil || r.Digits != 3 || r.Min != 1 || r.Max != 112 {
		t.Fatalf("unexpected rotation: %+v", r)
	}

	var rot usernameRotator
	seen := make(map[int]bool, 112)
	last := 0
	for i := 0; i < 112; i++ {
		got, err := rot.next(&cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, "24105157") || len(got) != len("24105157001") {
			t.Fatalf("unexpected username %q", got)
		}
		n, err := strconv.Atoi(got[len(got)-3:])
		if err != nil || n < 1 || n > 112 {
			t.Fatalf("suffix out of range in %q", got)
		}
		if seen[n] {
			t.Fatalf("suffix %03d repeated before the cycle ended", n)
		}
		seen[n] = true
		last = n
	}
	if len(seen) != 112 {
		t.Fatalf("saw %d distinct suffixes, want 112", len(seen))
	}

	got, err := rot.next(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(got[len(got)-3:])
	if n == last {
		t.Fatalf("new cycle started with the previous suffix %03d", n)
	}
}

func TestUsernameRotatorKeepsLeadingZeros(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = "REG000"
	cfg.UsernameRotation = &UsernameRotation{Enabled: true, Digits: 3, Min: 1, Max: 9}
	cfg.applyDefaults()

	var rot usernameRotator
	for i := 0; i < 9; i++ {
		got, err := rot.next(&cfg)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(got, "REG00") {
			t.Fatalf("leading zeros lost: %q", got)
		}
	}
}

func TestUsernameRotatorDisabled(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = "plainuser"
	cfg.UsernameRotation = nil

	var rot usernameRotator
	got, err := rot.next(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != cfg.Username {
		t.Fatalf("disabled rotation returned %q", got)
	}
}

func TestUsernameRotatorWithoutBase(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = ""
	cfg.Password = "test-pass"
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		t.Fatalf("empty username with rotation should validate: %v", err)
	}

	var rot usernameRotator
	seen := make(map[int]bool, 112)
	for i := 0; i < 112; i++ {
		got, err := rot.next(&cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != 3 {
			t.Fatalf("generated username %q is not a three-digit suffix", got)
		}
		n, err := strconv.Atoi(got)
		if err != nil || n < 1 || n > 112 {
			t.Fatalf("generated username out of range: %q", got)
		}
		if seen[n] {
			t.Fatalf("suffix %03d repeated before the cycle ended", n)
		}
		seen[n] = true
	}
	if len(seen) != 112 {
		t.Fatalf("saw %d distinct usernames, want 112", len(seen))
	}
}

func TestEmptyUsernameRequiresRotation(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = ""
	cfg.Password = "test-pass"
	cfg.UsernameRotation = nil
	cfg.applyDefaults()
	if err := cfg.validate(); err == nil {
		t.Fatal("expected an empty-username error when rotation is disabled")
	}
}

func TestFixedUsernameConfigIsValid(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = "plainuser"
	cfg.Password = "test-pass"
	cfg.UsernameRotation = nil
	cfg.applyDefaults()
	if err := cfg.validate(); err != nil {
		t.Fatalf("fixed config rejected: %v", err)
	}
}

func TestUsernameRotationZeroValueDefaults(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = "24105157001"
	cfg.Password = "test-pass"
	cfg.UsernameRotation = &UsernameRotation{Enabled: true}
	cfg.applyDefaults()

	r := cfg.UsernameRotation
	if r.Digits != 3 || r.Min != 1 || r.Max != 112 {
		t.Fatalf("unexpected defaults: %+v", *r)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("defaulted rotation rejected: %v", err)
	}
}

func TestUsernameRotationRejectsShortSuffix(t *testing.T) {
	cfg := defaultConfig()
	cfg.Username = "24105157X"
	cfg.Password = "test-pass"
	cfg.applyDefaults()
	if err := cfg.validate(); err == nil {
		t.Fatal("expected a trailing-digit error")
	}
}

func TestDefaultSuffixMaxFor(t *testing.T) {
	cases := map[int]int{
		0: 0,
		1: 9,
		2: 99,
		3: 112,
		4: 9999,
	}
	for digits, want := range cases {
		if got := defaultSuffixMaxFor(digits); got != want {
			t.Errorf("defaultSuffixMaxFor(%d) = %d, want %d", digits, got, want)
		}
	}
}
