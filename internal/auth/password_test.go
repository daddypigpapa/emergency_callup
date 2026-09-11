package auth

import (
	"strings"
	"testing"
	"unicode"
)

func TestGenerateRandomPassword_LengthAndAlphabet(t *testing.T) {
	seen := map[byte]bool{}
	for i := 0; i < 200; i++ {
		pw, err := GenerateRandomPassword()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if len(pw) != RandomPasswordLength {
			t.Fatalf("length = %d, want %d", len(pw), RandomPasswordLength)
		}
		for _, r := range pw {
			if strings.ContainsRune("0O1Il", r) {
				t.Fatalf("password %q contains confusing character %q", pw, r)
			}
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				t.Fatalf("password %q contains non-alphanumeric %q", pw, r)
			}
			seen[byte(r)] = true
		}
	}
	if len(seen) < 10 {
		t.Errorf("randomness looks suspicious: only %d distinct chars across 200 passwords", len(seen))
	}
}

func TestValidatePasswordPolicy(t *testing.T) {
	cases := []struct {
		name    string
		pw      string
		loginID string
		mobile  string
		wantErr error
	}{
		{"too short", "abc123", "k1234", "", ErrPasswordTooShort},
		{"contains login id", "myk1234pass", "k1234", "", ErrPasswordHasLoginID},
		{"contains mobile", "a01011112222", "k1234", "01011112222", ErrPasswordHasMobile},
		{"ok", "goodpass1", "k1234", "01011112222", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidatePasswordPolicy(c.pw, c.loginID, c.mobile)
			if err != c.wantErr {
				t.Errorf("got %v, want %v", err, c.wantErr)
			}
		})
	}
}
