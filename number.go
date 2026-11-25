package rally

import (
	"math/big"
	"math/bits"
	"strconv"
	"strings"
)

type decimal struct {
	neg    bool
	digits string
	exp    int
}

const maxDecimalExp = 1 << 30

func parseDecimal(s string) (decimal, bool) {
	var d decimal
	i := 0
	if i < len(s) && s[i] == '-' {
		d.neg = true
		i++
	}
	start := i
	switch {
	case i < len(s) && s[i] == '0':
		i++
	case i < len(s) && s[i] >= '1' && s[i] <= '9':
		for i < len(s) && isDigit(s[i]) {
			i++
		}
	default:
		return d, false
	}
	intPart := s[start:i]
	frac := ""
	if i < len(s) && s[i] == '.' {
		i++
		f := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == f {
			return d, false
		}
		frac = s[f:i]
	}
	exp := 0
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		negExp := false
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			negExp = s[i] == '-'
			i++
		}
		e := i
		for i < len(s) && isDigit(s[i]) {
			if exp < maxDecimalExp {
				exp = exp*10 + int(s[i]-'0')
			}
			i++
		}
		if i == e {
			return d, false
		}
		if negExp {
			exp = -exp
		}
	}
	if i != len(s) {
		return d, false
	}
	digits := strings.TrimLeft(intPart+frac, "0")
	trimmed := strings.TrimRight(digits, "0")
	d.exp = exp - len(frac) + len(digits) - len(trimmed)
	d.digits = trimmed
	if d.digits == "" {
		d.neg, d.exp = false, 0
	}
	return d, true
}

func (d decimal) cmp(o decimal) int {
	switch {
	case d.digits == "" && o.digits == "":
		return 0
	case d.digits == "":
		return o.sign() * -1
	case o.digits == "":
		return d.sign()
	case d.neg != o.neg:
		return d.sign()
	}
	c := 0
	if a, b := len(d.digits)+d.exp, len(o.digits)+o.exp; a != b {
		c = 1
		if a < b {
			c = -1
		}
	} else {
		c = strings.Compare(d.digits, o.digits)
	}
	if d.neg {
		return -c
	}
	return c
}

func (d decimal) sign() int {
	switch {
	case d.digits == "":
		return 0
	case d.neg:
		return -1
	}
	return 1
}

func (d decimal) integral() bool { return d.exp >= 0 }

func (d decimal) magnitude() (mag uint64, fits, frac bool) {
	n := len(d.digits) + d.exp
	if n <= 0 {
		return 0, true, d.digits != ""
	}
	if n > 20 {
		return 0, false, d.exp < 0
	}
	intDigits := d.digits
	if d.exp < 0 {
		intDigits = d.digits[:n]
	}
	u, err := strconv.ParseUint(intDigits+strings.Repeat("0", max(d.exp, 0)), 10, 64)
	return u, err == nil, d.exp < 0
}

func intOf[T int64 | uint64](d decimal) T {
	mag, _, _ := d.magnitude()
	n := T(mag)
	if d.neg {
		n = -n
	}
	return n
}

func intAtLeast[T int64 | uint64](d decimal, strict bool, lo, hi decimal) (T, bool) {
	if c := d.cmp(lo); c < 0 || c == 0 && !strict {
		return intOf[T](lo), true
	}
	if c := d.cmp(hi); c > 0 || c == 0 && strict {
		return intOf[T](hi), false
	}
	n, frac := intOf[T](d), !d.integral()
	if frac && d.neg {
		n--
	}
	if strict || frac {
		n++
	}
	return n, true
}

func intAtMost[T int64 | uint64](d decimal, strict bool, lo, hi decimal) (T, bool) {
	if c := d.cmp(hi); c > 0 || c == 0 && !strict {
		return intOf[T](hi), true
	}
	if c := d.cmp(lo); c < 0 || c == 0 && strict {
		return intOf[T](lo), false
	}
	n, frac := intOf[T](d), !d.integral()
	if frac && !d.neg {
		n++
	}
	if strict || frac {
		n--
	}
	return n, true
}

func decimalOf(s string) decimal {
	d, _ := parseDecimal(s)
	return d
}

func multipleOf(v, m decimal) bool {
	if v.digits == "" {
		return true
	}
	k := v.exp - m.exp
	if k < 0 {
		return false
	}
	if len(m.digits) <= 18 {
		b, _ := strconv.ParseUint(m.digits, 10, 64)
		r := uint64(0)
		for i := 0; i < len(v.digits); i++ {
			r = (r*10 + uint64(v.digits[i]-'0')) % b
		}
		return mulMod(r, powMod(10, k, b), b) == 0
	}
	b, _ := new(big.Int).SetString(m.digits, 10)
	r, chunk := new(big.Int), new(big.Int)
	for i := 0; i < len(v.digits); i += 18 {
		part := v.digits[i:min(i+18, len(v.digits))]
		c, _ := strconv.ParseUint(part, 10, 64)
		r.Mul(r, chunk.Exp(big.NewInt(10), big.NewInt(int64(len(part))), nil))
		r.Add(r, chunk.SetUint64(c)).Mod(r, b)
	}
	return r.Mul(r, chunk.Exp(big.NewInt(10), big.NewInt(int64(k)), b)).Mod(r, b).Sign() == 0
}

func mulMod(a, b, m uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	_, r := bits.Div64(hi, lo, m)
	return r
}

func powMod(x uint64, k int, m uint64) uint64 {
	r := uint64(1) % m
	x %= m
	for ; k > 0; k >>= 1 {
		if k&1 == 1 {
			r = mulMod(r, x, m)
		}
		x = mulMod(x, x, m)
	}
	return r
}

func (d decimal) integerStep() uint64 {
	p, _ := new(big.Int).SetString(d.digits, 10)
	if d.exp >= 0 {
		p.Mul(p, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(d.exp)), nil))
	} else {
		ten := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-d.exp)), nil)
		p.Quo(p, new(big.Int).GCD(nil, nil, p, ten))
	}
	if !p.IsUint64() {
		return 0
	}
	return p.Uint64()
}
