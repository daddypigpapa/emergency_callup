// Package roster manages the field-personnel roster (= accounts): member
// CRUD, xlsx import, and the SPEC §4.2 normalization rules.
package roster

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	ErrMobileInvalid   = errors.New("roster: mobile must normalize to 11 digits starting with 01")
	ErrOfficeTelLength = errors.New("roster: office phone must be exactly 4 digits")
	ErrTeamNoInvalid   = errors.New("roster: team number could not be parsed")
)

// errTextTooLong reports a length-limit violation for a named field
// (SPEC §4.2's per-field maximum lengths).
func errTextTooLong(field string, max int) error {
	return fmt.Errorf("roster: %s exceeds maximum length of %d", field, max)
}

// Max lengths per SPEC §4.2.
const (
	MaxNameLen    = 30
	MaxDeptLen    = 50
	MaxMissionLen = 200
	MaxNoteLen    = 200
)

func digitsOnly(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// NormalizeMobile implements SPEC §4.2's mobile-number rule:
//   - keep digits only
//   - a 10-digit number starting with "10" gets a leading zero restored
//     (Excel numeric-cell corruption: "01012345678" stored as the number
//     1012345678, dropping the leading zero) — this path also reports a
//     warning so the operator can double-check the source data.
//   - anything else must already be 11 digits matching 01[0-9]{9}, or it's
//     rejected outright.
func NormalizeMobile(raw string) (normalized string, warned bool, err error) {
	d := digitsOnly(raw)
	if len(d) == 10 && strings.HasPrefix(d, "10") {
		d = "0" + d
		warned = true
	}
	if !isValidMobile(d) {
		return "", false, ErrMobileInvalid
	}
	return d, warned, nil
}

func isValidMobile(d string) bool {
	if len(d) != 11 {
		return false
	}
	if d[0] != '0' || d[1] != '1' {
		return false
	}
	for _, c := range d {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// NormalizeOfficeTel implements SPEC §4.2: digits only, must end up exactly
// 4 characters (leading zero preserved because it's kept as a string), or
// be empty (optional field).
func NormalizeOfficeTel(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", nil
	}
	d := digitsOnly(raw)
	if len(d) != 4 {
		return "", ErrOfficeTelLength
	}
	return d, nil
}

// NormalizeTeamNo implements SPEC §4.2: accepts "3", "3조", "03" -> 3.
// Existence in the `team` table is checked by the caller (this package has
// no team-table dependency), per SPEC §4.2 "team 테이블에 없으면 거부".
func NormalizeTeamNo(raw string) (int, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimSuffix(trimmed, "조")
	trimmed = strings.TrimSpace(trimmed)
	d := digitsOnly(trimmed)
	if d == "" {
		return 0, ErrTeamNoInvalid
	}
	n, err := strconv.Atoi(d)
	if err != nil {
		return 0, ErrTeamNoInvalid
	}
	return n, nil
}

// stripControlChars removes C0/C1 control characters (SPEC §4.2:
// "제어문자 제거").
func stripControlChars(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '\t' || r == '\n' || r == '\r' {
			b.WriteRune(' ')
			continue
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NormalizeText trims, strips control characters, and enforces a maximum
// rune length (SPEC §4.2).
func NormalizeText(raw string, maxLen int, fieldName string) (string, error) {
	s := strings.TrimSpace(stripControlChars(raw))
	if len([]rune(s)) > maxLen {
		return "", errTextTooLong(fieldName, maxLen)
	}
	return s, nil
}
