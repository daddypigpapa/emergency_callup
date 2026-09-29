// Package xlsxin holds the plumbing shared by the admin spreadsheet
// importers (임무지역, 조별계획): signature-checked opening, header checks,
// cell access, and the short-lived preview-token store behind the
// two-step preview → commit flow (SPEC §7.3).
package xlsxin

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/xuri/excelize/v2"
)

var (
	ErrBadSignature   = errors.New("xlsx 파일이 아닙니다")
	ErrPreviewExpired = errors.New("미리보기가 만료되었거나 존재하지 않습니다")
)

// Open parses an uploaded workbook after checking the ZIP magic bytes
// (SPEC §11.3: extension alone isn't trusted).
func Open(data []byte) (*excelize.File, error) {
	if !bytes.HasPrefix(data, []byte{'P', 'K', 0x03, 0x04}) {
		return nil, ErrBadSignature
	}
	return excelize.OpenReader(bytes.NewReader(data))
}

// Sheet returns a sheet's rows, requiring its first row to start with want.
func Sheet(f *excelize.File, name string, want []string) ([][]string, error) {
	rows, err := f.GetRows(name)
	if err != nil {
		return nil, fmt.Errorf("'%s' 시트가 없습니다 — 표준 서식을 사용하세요", name)
	}
	if len(rows) == 0 || !HeaderMatches(rows[0], want) {
		return nil, fmt.Errorf("'%s' 시트의 첫 줄(제목)이 표준 서식과 다릅니다: %s", name, strings.Join(want, ", "))
	}
	return rows, nil
}

func HeaderMatches(got, want []string) bool {
	if len(got) < len(want) {
		return false
	}
	for i, w := range want {
		if strings.TrimSpace(got[i]) != w {
			return false
		}
	}
	return true
}

// Cell returns the trimmed value at index i, or "" past the row's end.
func Cell(row []string, i int) string {
	if i < len(row) {
		return strings.TrimSpace(row[i])
	}
	return ""
}

func IsBlank(row []string) bool {
	for _, c := range row {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

// Float parses a cell as a number, tolerating thousands separators.
// ok=false for a blank cell; err for a non-blank non-number.
func Float(s string) (v float64, ok bool, err error) {
	s = strings.ReplaceAll(strings.TrimSpace(s), ",", "")
	if s == "" {
		return 0, false, nil
	}
	v, err = strconv.ParseFloat(s, 64)
	return v, err == nil, err
}

// Previews keeps parsed-but-uncommitted batches for a short while, keyed by
// an unguessable single-use token.
type Previews[T any] struct {
	mu    sync.Mutex
	items map[string]entry[T]
	ttl   time.Duration
}

type entry[T any] struct {
	v  T
	at time.Time
}

func NewPreviews[T any](ttl time.Duration) *Previews[T] {
	return &Previews[T]{items: map[string]entry[T]{}, ttl: ttl}
}

func (p *Previews[T]) Put(v T, now time.Time) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	token := hex.EncodeToString(b)
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, e := range p.items {
		if now.Sub(e.at) > p.ttl {
			delete(p.items, k)
		}
	}
	p.items[token] = entry[T]{v: v, at: now}
	return token
}

// Take returns and removes the batch for token (single use).
func (p *Previews[T]) Take(token string, now time.Time) (T, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e, ok := p.items[token]
	delete(p.items, token)
	if !ok || now.Sub(e.at) > p.ttl {
		var zero T
		return zero, ErrPreviewExpired
	}
	return e.v, nil
}
