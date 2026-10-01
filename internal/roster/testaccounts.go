package roster

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
)

// bom is the UTF-8 byte-order mark Windows editors (Notepad) prepend.
const bom = string(rune(0xFEFF))

// TestAccount is one "id=password" line from the TEST_TOOLS accounts file
// (docs/SPEC.md §12.3).
type TestAccount struct {
	Line     int
	LoginID  string
	Password string
}

// ParseTestAccounts reads "id=password" lines. Blank lines and lines
// starting with # are skipped; the password is everything after the first
// "=", so it may itself contain "=". Problems never echo the line's
// content, since that would put a password into the server log.
func ParseTestAccounts(r io.Reader) (accounts []TestAccount, problems []string, err error) {
	sc := bufio.NewScanner(r)
	n := 0
	for sc.Scan() {
		n++
		line := strings.TrimSpace(sc.Text())
		if n == 1 {
			line = strings.TrimPrefix(line, bom) // BOM from Windows editors
		}
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		id, pw, ok := strings.Cut(line, "=")
		id, pw = strings.TrimSpace(id), strings.TrimSpace(pw)
		if !ok || id == "" || pw == "" {
			problems = append(problems, fmt.Sprintf("line %d: expected id=password", n))
			continue
		}
		accounts = append(accounts, TestAccount{Line: n, LoginID: id, Password: pw})
	}
	return accounts, problems, sc.Err()
}

// TestPasswordMinLen is the looser length rule for TEST_TOOLS accounts
// (short PINs are convenient when testing on phones). Real accounts keep
// the 8-character policy in auth.ValidatePasswordPolicy.
const TestPasswordMinLen = 4

// ApplyTestAccounts sets each listed member's password (no forced change on
// next login) and reports how many were applied plus a problem per line
// that wasn't: unknown login ID, or a password shorter than
// TestPasswordMinLen.
func (s *Store) ApplyTestAccounts(ctx context.Context, accounts []TestAccount) (applied int, problems []string) {
	for _, a := range accounts {
		m, err := s.GetByLoginID(ctx, a.LoginID)
		if errors.Is(err, ErrNotFound) {
			problems = append(problems, fmt.Sprintf("line %d: no field member with id %q", a.Line, a.LoginID))
			continue
		}
		if err != nil {
			problems = append(problems, fmt.Sprintf("line %d (%s): %v", a.Line, a.LoginID, err))
			continue
		}
		if len(a.Password) < TestPasswordMinLen {
			problems = append(problems, fmt.Sprintf("line %d (%s): password must be at least %d characters", a.Line, a.LoginID, TestPasswordMinLen))
			continue
		}
		if err := s.storePassword(ctx, m.ID, a.Password, false); err != nil {
			problems = append(problems, fmt.Sprintf("line %d (%s): %v", a.Line, a.LoginID, err))
			continue
		}
		applied++
	}
	return applied, problems
}
