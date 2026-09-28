package clientpolicy

import (
	"errors"
	"math"
	"math/bits"
	"strconv"
	"strings"
)

const MultiplierScale uint64 = 1_000_000
const MaxMultiplier uint64 = 1_000 * MultiplierScale

var (
	ErrInvalidPolicy    = errors.New("invalid client policy")
	ErrInvalidUsage     = errors.New("invalid client usage")
	ErrInvalidDirection = errors.New("invalid traffic direction")
	ErrOverflow         = errors.New("client accounting overflow")
)

type Direction uint8

const (
	Upload Direction = iota
	Download
)

type Usage struct {
	RawUpload   uint64 `json:"rawUpload"`
	RawDownload uint64 `json:"rawDownload"`
	BilledBytes uint64 `json:"billedBytes"`
	Remainder   uint64 `json:"remainder"`
}

func ParseMultiplier(s string) (uint64, error) {
	whole, fraction, fractional := strings.Cut(s, ".")
	if whole == "" || len(fraction) > 6 || fractional && fraction == "" {
		return 0, ErrInvalidPolicy
	}
	for _, part := range []string{whole, fraction} {
		for _, c := range part {
			if c < '0' || c > '9' {
				return 0, ErrInvalidPolicy
			}
		}
	}
	n, err := strconv.ParseUint(whole, 10, 64)
	if err != nil || n > 1000 {
		return 0, ErrInvalidPolicy
	}
	f, err := strconv.ParseUint(fraction+strings.Repeat("0", 6-len(fraction)), 10, 64)
	if err != nil {
		return 0, ErrInvalidPolicy
	}
	m := n*MultiplierScale + f
	if m == 0 || m > MaxMultiplier {
		return 0, ErrInvalidPolicy
	}
	return m, nil
}

func charge(before Usage, direction Direction, n, multiplier uint64) (Usage, error) {
	if direction != Upload && direction != Download {
		return before, ErrInvalidDirection
	}
	if multiplier == 0 || multiplier > MaxMultiplier {
		return before, ErrInvalidPolicy
	}
	if before.Remainder >= MultiplierScale {
		return before, ErrInvalidUsage
	}
	result := before
	raw := &result.RawUpload
	if direction == Download {
		raw = &result.RawDownload
	}
	if n > math.MaxUint64-*raw {
		return before, ErrOverflow
	}
	hi, lo := bits.Mul64(n, multiplier)
	lo, carry := bits.Add64(lo, before.Remainder, 0)
	hi, carry = bits.Add64(hi, 0, carry)
	if carry != 0 || hi >= MultiplierScale {
		return before, ErrOverflow
	}
	billed, remainder := bits.Div64(hi, lo, MultiplierScale)
	if billed > math.MaxUint64-before.BilledBytes {
		return before, ErrOverflow
	}
	*raw += n
	result.BilledBytes += billed
	result.Remainder = remainder
	return result, nil
}
