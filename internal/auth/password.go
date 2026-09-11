package auth

import (
	"crypto/rand"
	"errors"
	"math/big"
	"strings"
)

// confusingCharsExcluded per SPEC §11.1 ("무작위 10자(혼동 문자 제외)"):
// no 0/O, 1/I/l, and no lowercase o to avoid confusion with 0.
const passwordAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghjkmnpqrstuvwxyz"

const RandomPasswordLength = 10

// GenerateRandomPassword returns a cryptographically random password of
// RandomPasswordLength characters drawn from an alphabet with confusing
// characters removed.
func GenerateRandomPassword() (string, error) {
	b := make([]byte, RandomPasswordLength)
	alphabetLen := big.NewInt(int64(len(passwordAlphabet)))
	for i := range b {
		n, err := rand.Int(rand.Reader, alphabetLen)
		if err != nil {
			return "", err
		}
		b[i] = passwordAlphabet[n.Int64()]
	}
	return string(b), nil
}

var (
	ErrPasswordTooShort   = errors.New("auth: password must be at least 8 characters")
	ErrPasswordHasLoginID = errors.New("auth: password must not contain the login ID")
	ErrPasswordHasMobile  = errors.New("auth: password must not contain the mobile number")
)

// ValidatePasswordPolicy enforces SPEC §11.1 ("8자 이상, 로그인ID·휴대전화
// 포함 금지"). loginID and mobile may be passed empty when not applicable
// (e.g. admin accounts have no mobile).
func ValidatePasswordPolicy(password, loginID, mobile string) error {
	if len(password) < 8 {
		return ErrPasswordTooShort
	}
	lower := strings.ToLower(password)
	if loginID != "" && strings.Contains(lower, strings.ToLower(loginID)) {
		return ErrPasswordHasLoginID
	}
	if mobile != "" && strings.Contains(password, mobile) {
		return ErrPasswordHasMobile
	}
	return nil
}
