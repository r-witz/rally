package rally

import (
	"math/big"
	"testing"
)

func TestNumberTextGrammar(t *testing.T) {
	for s, want := range map[string][2]bool{
		"1": {true, true}, "-0": {true, true}, "1.5": {false, true}, "1e+5": {false, true}, "1E-5": {false, true},
		"1.": {false, false}, "1e": {false, false}, "1e+": {false, false}, "": {false, false}, "-": {false, false}, "01": {false, false}, " 1": {false, false},
	} {
		if _, ok := parseDecimal(s); ok != want[1] {
			t.Errorf("parseDecimal(%q) = %v, want %v", s, ok, want[1])
		}
	}
	if !queryKeyIs("%61b", "ab") || !queryKeyIs("%4a", "J") || queryKeyIs("%4", "J") || splitCSV("") != nil {
		t.Error("query key decoding")
	}
}

func TestDecimal(t *testing.T) {
	for _, c := range []struct {
		a, b string
		cmp  int
	}{
		{"1", "1.0", 0}, {"1e2", "100", 0}, {"0.5e1", "5", 0}, {"-0", "0", 0}, {"0.0e10", "-0.0", 0},
		{"18446744073709551616", "18446744073709551615", 1}, {"-18446744073709551616", "-18446744073709551615", -1},
		{"0.123456789123456788", "0.123456789123456789", -1}, {"0.1234567891234567891", "0.123456789123456789", 1},
		{"-1", "1", -1}, {"0", "-1e-400", 1}, {"1e-400", "0", 1}, {"9.99", "10", -1}, {"10", "9.99", 1}, {"-10", "-9.99", -1},
	} {
		a, okA := parseDecimal(c.a)
		b, okB := parseDecimal(c.b)
		if !okA || !okB {
			t.Fatalf("%s or %s not parsed", c.a, c.b)
		}
		if got := a.cmp(b); got != c.cmp {
			t.Errorf("cmp(%s, %s) = %d, want %d", c.a, c.b, got, c.cmp)
		}
	}
	for _, s := range []string{"", "-", "01", "1.", ".1", "1e", "1e+", "+1", "1x", "0x1", "NaN", "Infinity", "1 "} {
		if _, ok := parseDecimal(s); ok {
			t.Errorf("parseDecimal(%q) accepted", s)
		}
	}
	lo, hi := decimalOf("-128"), decimalOf("127")
	for _, c := range []struct {
		s               string
		atLeast, strict bool
		want            int64
		ok              bool
	}{
		{"1.5", true, false, 2, true}, {"1.5", false, false, 1, true}, {"-1.5", true, false, -1, true}, {"-1.5", false, false, -2, true},
		{"1e30", false, false, 127, true}, {"-1e30", true, false, -128, true}, {"2", true, false, 2, true}, {"0.1", false, false, 0, true}, {"-0.1", true, false, 0, true},
		{"2", true, true, 3, true}, {"2", false, true, 1, true}, {"-1.5", true, true, -1, true}, {"-1.5", false, true, -2, true}, {"126.5", true, true, 127, true},
		{"127", true, true, 127, false}, {"200", true, false, 127, false}, {"-128", false, true, -128, false}, {"-128", true, true, -127, true},
	} {
		got, ok := intAtMost[int64](decimalOf(c.s), c.strict, lo, hi)
		if c.atLeast {
			got, ok = intAtLeast[int64](decimalOf(c.s), c.strict, lo, hi)
		}
		if got != c.want || ok != c.ok {
			t.Errorf("atLeast=%v strict=%v of %s = %d, %v, want %d, %v", c.atLeast, c.strict, c.s, got, ok, c.want, c.ok)
		}
	}
}

func TestMultipleOf(t *testing.T) {
	for _, c := range []struct {
		v, m string
		want bool
	}{
		{"0.0075", "0.0001", true}, {"0.00751", "0.0001", false}, {"1e308", "0.5", true}, {"4.5", "1.5", true}, {"35", "1.5", false},
		{"0", "7", true}, {"-21", "7", true}, {"22", "7", false}, {"1e-400", "1", false}, {"1e400", "3", false}, {"3e400", "3", true},
		{"19.99", "0.01", true}, {"0.3", "0.1", true}, {"1", "0.3", false}, {"123456789012345678901234567890", "10", true},
		{"1", "1", true}, {"36", "12345678901234567890", false}, {"24691357802469135780", "12345678901234567890", true},
		{"24691357802469135780e3", "1234567890123456789e1", true}, {"2469135780246913578e1", "12345678901234567891", false},
		{"18446744073709551615", "999999999999999999", false}, {"999999999999999999000", "999999999999999999", true},
	} {
		if got := multipleOf(decimalOf(c.v), decimalOf(c.m)); got != c.want {
			t.Errorf("multipleOf(%s, %s) = %v, want %v", c.v, c.m, got, c.want)
		}
	}
}

func FuzzDecimal(f *testing.F) {
	for _, s := range [][2]string{{"1", "1.0"}, {"0.0075", "0.0001"}, {"-1e2", "5"}, {"18446744073709551616", "3"}, {"0.1", "1e-1"}, {"24691357802469135780", "12345678901234567890"}, {"0", "1E10000000"}, {"0e11000000000000000000000000", "0"}} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		da, okA := parseDecimal(a)
		db, okB := parseDecimal(b)
		if !okA || !okB || len(a) > 60 || len(b) > 60 || max(da.exp, -da.exp, db.exp, -db.exp) > 400 {
			return
		}
		ra, okA := new(big.Rat).SetString(a)
		rb, okB := new(big.Rat).SetString(b)
		if !okA || !okB {
			return
		}
		if got, want := da.cmp(db), ra.Cmp(rb); got != want {
			t.Fatalf("cmp(%s, %s) = %d, want %d", a, b, got, want)
		}
		if got, want := da.integral(), ra.IsInt(); got != want {
			t.Fatalf("integral(%s) = %v, want %v", a, got, want)
		}
		if rb.Sign() > 0 {
			if got, want := multipleOf(da, db), new(big.Rat).Quo(ra, rb).IsInt(); got != want {
				t.Fatalf("multipleOf(%s, %s) = %v, want %v", a, b, got, want)
			}
		}
		if mag, fits, frac := da.magnitude(); fits {
			want := new(big.Int).Quo(new(big.Int).Abs(ra.Num()), ra.Denom())
			if !want.IsUint64() || want.Uint64() != mag || frac == ra.IsInt() {
				t.Fatalf("magnitude(%s) = %d %v, want %v %v", a, mag, frac, want, !ra.IsInt())
			}
		}
	})
}
