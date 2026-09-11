package roster

import "testing"

// R4: 010-1111-2222, 01011112222, and the numeric-cell-corrupted
// 1011112222 must all normalize to the same 11-digit number, with the last
// one flagged as a warning.
func TestNormalizeMobile_R4(t *testing.T) {
	cases := []struct {
		in       string
		wantWarn bool
	}{
		{"010-1111-2222", false},
		{"01011112222", false},
		{"1011112222", true},
	}
	for _, c := range cases {
		got, warned, err := NormalizeMobile(c.in)
		if err != nil {
			t.Fatalf("NormalizeMobile(%q): %v", c.in, err)
		}
		if got != "01011112222" {
			t.Errorf("NormalizeMobile(%q) = %q, want 01011112222", c.in, got)
		}
		if warned != c.wantWarn {
			t.Errorf("NormalizeMobile(%q) warned = %v, want %v", c.in, warned, c.wantWarn)
		}
	}
}

func TestNormalizeMobile_Rejects(t *testing.T) {
	bad := []string{"", "0201234567", "010123456", "02-1234-5678", "011112222233"}
	for _, in := range bad {
		if _, _, err := NormalizeMobile(in); err == nil {
			t.Errorf("NormalizeMobile(%q) should be rejected", in)
		}
	}
}

func TestNormalizeOfficeTel(t *testing.T) {
	got, err := NormalizeOfficeTel("0123")
	if err != nil || got != "0123" {
		t.Errorf("got %q, %v", got, err)
	}
	got, err = NormalizeOfficeTel("")
	if err != nil || got != "" {
		t.Errorf("empty should be allowed, got %q, %v", got, err)
	}
	if _, err := NormalizeOfficeTel("12345"); err != ErrOfficeTelLength {
		t.Errorf("5 digits should be rejected, got %v", err)
	}
	if _, err := NormalizeOfficeTel("12"); err != ErrOfficeTelLength {
		t.Errorf("2 digits should be rejected, got %v", err)
	}
}

func TestNormalizeTeamNo(t *testing.T) {
	cases := map[string]int{"3": 3, "3조": 3, "03": 3, " 10조 ": 10, "10": 10}
	for in, want := range cases {
		got, err := NormalizeTeamNo(in)
		if err != nil {
			t.Errorf("NormalizeTeamNo(%q): %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("NormalizeTeamNo(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := NormalizeTeamNo("조"); err == nil {
		t.Error("bare '조' with no digits should be rejected")
	}
	if _, err := NormalizeTeamNo(""); err == nil {
		t.Error("empty should be rejected")
	}
}

func TestNormalizeText_TrimAndControlChars(t *testing.T) {
	got, err := NormalizeText("  김도현\x00\x01  ", MaxNameLen, "이름")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "김도현" {
		t.Errorf("got %q, want 김도현", got)
	}
}

func TestNormalizeText_TooLong(t *testing.T) {
	long := make([]rune, MaxNameLen+1)
	for i := range long {
		long[i] = '가'
	}
	if _, err := NormalizeText(string(long), MaxNameLen, "이름"); err == nil {
		t.Error("expected length error")
	}
}
