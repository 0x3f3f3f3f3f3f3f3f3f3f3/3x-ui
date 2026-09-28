package clientpolicy

import (
	"errors"
	"math"
	"testing"
)

func TestMultiplierRejectsAmbiguousOrUnboundedValues(t *testing.T) {
	for _, s := range []string{"", "0", "-1", "+1", "NaN", "Inf", "1e2", " 1", "1 ", ".5", "1.", "0.0000001", "1000.000001", "9999999999999999999999999"} {
		if _, err := ParseMultiplier(s); !errors.Is(err, ErrInvalidPolicy) {
			t.Errorf("ParseMultiplier(%q) error = %v, want ErrInvalidPolicy", s, err)
		}
	}
	for _, tc := range []struct {
		s    string
		want uint64
	}{
		{"0.5", 500000}, {"1", 1000000}, {"1.5", 1500000}, {"2", 2000000}, {"10", 10000000}, {"0.000001", 1}, {"1000.000000", 1000000000},
	} {
		got, err := ParseMultiplier(tc.s)
		if err != nil || got != tc.want {
			t.Errorf("ParseMultiplier(%q) = %d, %v; want %d", tc.s, got, err, tc.want)
		}
	}
}

func TestChargePreservesRemainderAcrossBatchesAndDirections(t *testing.T) {
	for _, tc := range []struct {
		multiplier        uint64
		billed, remainder uint64
	}{
		{500000, 3, 500000}, {1000000, 7, 0}, {1500000, 10, 500000}, {2000000, 14, 0}, {10000000, 70, 0},
	} {
		u := Usage{}
		for _, n := range []uint64{1, 2, 4} {
			var err error
			u, err = charge(u, Upload, n, tc.multiplier)
			if err != nil {
				t.Fatal(err)
			}
		}
		if u != (Usage{RawUpload: 7, BilledBytes: tc.billed, Remainder: tc.remainder}) {
			t.Fatalf("split charge = %+v", u)
		}
		whole, err := charge(Usage{}, Download, 7, tc.multiplier)
		if err != nil || whole != (Usage{RawDownload: 7, BilledBytes: tc.billed, Remainder: tc.remainder}) {
			t.Fatalf("whole charge = %+v, %v", whole, err)
		}
	}
	u, _ := charge(Usage{}, Upload, 1, 500000)
	u, err := charge(u, Download, 1, 500000)
	if err != nil || u != (Usage{RawUpload: 1, RawDownload: 1, BilledBytes: 1}) {
		t.Fatalf("bidirectional remainder lost: %+v %v", u, err)
	}
}

func TestMultiplierChangeDoesNotRepriceHistory(t *testing.T) {
	u, err := charge(Usage{}, Upload, 10<<30, 1000000)
	if err != nil {
		t.Fatal(err)
	}
	u, err = charge(u, Download, 5<<30, 2000000)
	if err != nil || u != (Usage{RawUpload: 10737418240, RawDownload: 5368709120, BilledBytes: 21474836480}) {
		t.Fatalf("changed multiplier repriced history: %+v %v", u, err)
	}
	u, _ = charge(Usage{}, Upload, 1, 500000)
	u, err = charge(u, Upload, 1, 1500000)
	if err != nil || u != (Usage{RawUpload: 2, BilledBytes: 2}) {
		t.Fatalf("version change lost fraction: %+v %v", u, err)
	}
}

func TestChargeRejectsOverflowWithoutMutatingUsage(t *testing.T) {
	for _, tc := range []struct {
		usage         Usage
		direction     Direction
		n, multiplier uint64
		want          error
	}{
		{Usage{RawUpload: math.MaxUint64}, Upload, 1, 1000000, ErrOverflow},
		{Usage{BilledBytes: math.MaxUint64}, Download, 1, 1000000, ErrOverflow},
		{Usage{}, Upload, math.MaxUint64, 2000000, ErrOverflow},
		{Usage{Remainder: 1000000}, Upload, 1, 1000000, ErrInvalidUsage},
		{Usage{}, Direction(5), 1, 1000000, ErrInvalidDirection},
		{Usage{}, Upload, 1, 0, ErrInvalidPolicy},
	} {
		got, err := charge(tc.usage, tc.direction, tc.n, tc.multiplier)
		if !errors.Is(err, tc.want) || got != tc.usage {
			t.Fatalf("charge = %+v, %v; want unchanged %+v, %v", got, err, tc.usage, tc.want)
		}
	}
	u, err := charge(Usage{}, Upload, math.MaxUint64, 1)
	if err != nil || u.BilledBytes != 18446744073709 || u.Remainder != 551615 {
		t.Fatalf("wide multiplication failed: %+v %v", u, err)
	}
}

func FuzzChargeBatchInvariant(f *testing.F) {
	f.Add(uint64(7), uint64(3), uint64(1500000))
	f.Add(uint64(1000000001), uint64(500000000), uint64(1))
	f.Fuzz(func(t *testing.T, total, split, multiplier uint64) {
		total %= 1 << 40
		split %= total + 1
		multiplier = multiplier%1000000000 + 1
		whole, err := charge(Usage{}, Upload, total, multiplier)
		if err != nil {
			t.Fatal(err)
		}
		parts, err := charge(Usage{}, Upload, split, multiplier)
		if err != nil {
			t.Fatal(err)
		}
		parts, err = charge(parts, Upload, total-split, multiplier)
		if err != nil || whole != parts {
			t.Fatalf("batch dependent accounting: whole=%+v split=%+v error=%v", whole, parts, err)
		}
	})
}
