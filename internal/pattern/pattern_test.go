package pattern

import (
	"math/rand/v2"
	"regexp"
	"slices"
	"strings"
	"testing"
)

type compiled struct {
	*Pattern
	forward *Pattern
}

func compileEveryWay(source string) (compiled, error) {
	p, err := Compile(source)
	if err != nil {
		return compiled{}, err
	}
	forward := newPattern(source, false)
	forward.fits(maxPatternTable)
	forward.readBackward(source, maxPatternTable)
	forward.summarize()
	return compiled{p, forward}, nil
}

func checkPattern(t *testing.T, p compiled, source, s string, want bool) {
	t.Helper()
	forward := p.forward
	if got := p.Match(s); got != want {
		t.Errorf("pattern %q, string %q: got %v, want %v", source, s, got, want)
	}
	if got := forward.simulate(s); got != want {
		t.Errorf("pattern %q, string %q: simulated %v, want %v", source, s, got, want)
	}
	if forward.table != nil && forward.Match(s) != want {
		t.Errorf("pattern %q, string %q: read forward %v, want %v", source, s, !want, want)
	}
	if forward.backward != nil && forward.backward.matchesBackward(s) != want {
		t.Errorf("pattern %q, string %q: read backward %v, want %v", source, s, !want, want)
	}
	if want && (len(s) < forward.least || forward.required >= 0 && !strings.ContainsRune(s, forward.required)) {
		t.Errorf("pattern %q, string %q: a match needs %d bytes and %q", source, s, forward.least, forward.required)
	}
}

func TestPatternsFollowECMAScript(t *testing.T) {
	for _, c := range ecmaScriptPatterns {
		p, err := compileEveryWay(c.pattern)
		if err != nil {
			t.Errorf("pattern %q: %v", c.pattern, err)
			continue
		}
		if p.table == nil && p.backward == nil && !p.literal {
			t.Errorf("pattern %q has no table", c.pattern)
		}
		for _, s := range c.accepts {
			checkPattern(t, p, c.pattern, s, true)
		}
		for _, s := range c.rejects {
			checkPattern(t, p, c.pattern, s, false)
		}
	}
}

func TestPatternsRejected(t *testing.T) {
	for _, source := range slices.Concat(ecmaScriptSyntaxErrors, unsupportedPatterns) {
		if _, err := Compile(source); err == nil {
			t.Errorf("pattern %q was accepted", source)
		}
	}
}

var (
	randomAtoms       = []string{"a", "b", "1", "-", " ", `\d`, `\D`, `\w`, `\W`, "[ab]", "[^a]", "[a-b1]", `[\d-]`, `[^\w]`}
	randomAssertions  = []string{"^", "$", `\b`, `\B`}
	randomQuantifiers = []string{"", "", "", "", "*", "+", "?", "{2}", "{1,2}", "{0,}", "*?", "+?", "??", "{1,2}?", "{0,3}", "{1,4}", "{2,5}", "{0,2}?"}

	repeatedAtoms = []string{"a", "b", " ", "[ab]", "ab", "ba", "a?", "b*", "a+", "a|b", "ab|a", "a|ab", "aa|a|ab", "b|[ab]b", "a b?", "^", "$", `\b`}
	repetitions   = []string{"", "", "?", "*", "+", "{2}", "{0,2}", "{0,3}", "{1,3}", "{1,4}", "{2,4}", "{0,5}"}
	ambiguous     = []string{`^(a+b?){1,4}$`, `(a|ab){1,3}b`, `^.{1,3}b.{1,3}$`, `^((a|b){1,3}a?){2,4}b$`, `^(?:aa|a|ab){0,3}$`,
		`\b(a{1,3} ?){1,3}\b`, `^((a{1,3}){1,3}){1,3}$`, `(a{0,3}$|b){1,3}`, `^(?:(?:a|ab){0,3}b){0,3}$`, `a{0,3}b`, `^(?:a*b){1,3}a{0,2}$`}
)

func pick(r *rand.Rand, choices []string) string { return choices[r.IntN(len(choices))] }

func randomPattern(r *rand.Rand, depth int) string {
	var b strings.Builder
	for range 1 + r.IntN(4) {
		switch choice := r.IntN(12); {
		case choice == 0:
			b.WriteString("|")
		case choice == 1:
			b.WriteString(pick(r, randomAssertions))
		case choice <= 3 && depth > 0:
			b.WriteString(pick(r, []string{"(", "(?:"}) + randomPattern(r, depth-1) + ")" + pick(r, randomQuantifiers))
		default:
			b.WriteString(pick(r, randomAtoms) + pick(r, randomQuantifiers))
		}
	}
	return b.String()
}

func iterations(full int) int {
	if testing.Short() {
		return full / 10
	}
	return full
}

func TestPatternsAgreeWithRegexp(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	for range iterations(20000) {
		source := randomPattern(r, 3)
		p, err := compileEveryWay(source)
		if err != nil {
			t.Fatalf("pattern %q: %v", source, err)
		}
		re, err := regexp.Compile(source)
		if err != nil {
			continue
		}
		for range 20 {
			s := make([]byte, r.IntN(12))
			for i := range s {
				s[i] = "ab1- _"[r.IntN(6)]
			}
			checkPattern(t, p, source, string(s), re.MatchString(string(s)))
		}
	}
}

func randomRepetitions(r *rand.Rand, depth int) string {
	var b strings.Builder
	for range 1 + r.IntN(3) {
		inner := pick(r, repeatedAtoms)
		if depth > 0 && r.IntN(2) == 0 {
			inner = randomRepetitions(r, depth-1)
		}
		b.WriteString("(?:" + inner + ")" + pick(r, repetitions))
	}
	return b.String()
}

func eachString(alphabet string, length int, visit func(string)) {
	var extend func(s []byte)
	extend = func(s []byte) {
		visit(string(s))
		if len(s) < length {
			for i := range len(alphabet) {
				extend(append(s, alphabet[i]))
			}
		}
	}
	extend(nil)
}

func TestRepetitionsAgreeWithRegexp(t *testing.T) {
	r := rand.New(rand.NewPCG(5, 6))
	sources := slices.Clone(ambiguous)
	for range iterations(4000) {
		sources = append(sources, randomRepetitions(r, 2))
	}
	for i, source := range sources {
		re, err := regexp.Compile(source)
		if err != nil {
			continue
		}
		p, err := compileEveryWay(source)
		if err != nil {
			t.Fatalf("pattern %q: %v", source, err)
		}
		length := 5
		if i < len(ambiguous) {
			length = 8
		}
		eachString("ab ", length, func(s string) { checkPattern(t, p, source, s, re.MatchString(s)) })
	}
}

func TestLongRepetitionsAgreeWithRegexp(t *testing.T) {
	r := rand.New(rand.NewPCG(7, 8))
	atoms := []string{"a", "b", "[ab]", ".", "ab", "a|b", "ab|a", "a+", "b?", "a b?", "(?:ab)+", "x"}
	counts := []string{"", "", "*", "+", "{40}", "{64}", "{0,70}", "{30,90}", "{1,130}", "{65,}"}
	for range iterations(400) {
		source := pick(r, []string{"", "", "^"})
		for range 1 + r.IntN(3) {
			source += "(?:" + pick(r, atoms) + ")" + pick(r, counts)
		}
		source += pick(r, []string{"", "", "$"})
		re, err := regexp.Compile(source)
		if err != nil {
			continue
		}
		p, err := compileEveryWay(source)
		if err != nil {
			t.Fatalf("pattern %q: %v", source, err)
		}
		for range 40 {
			s := make([]byte, r.IntN(300))
			for i := range s {
				s[i] = "aaabbb x"[r.IntN(8)]
			}
			checkPattern(t, p, source, string(s), re.MatchString(string(s)))
		}
	}
}

func TestAmbiguousRepetitionsGetTables(t *testing.T) {
	for _, c := range []struct {
		pattern string
		cells   int
	}{
		{`.{1,1000}@.{1,1000}`, 100}, {`^(\w+\s?){1,300}$`, 4000}, {`^(?:\S+ ?){1,100}$`, 1000}, {`^.{1,64}@.{1,255}$`, 20000},
		{`(a{1,30}b?){1,30}$`, 100}, {`^(?:[a-z]+,?){1,50}$`, 500}, {`[^a]*1{0,2}\d{0,2}\W{2}`, 66}, {`a{0,5}b`, 20},
		{`^(?:[a-zA-Z0-9-]{1,63}\.)+[a-zA-Z]{2,63}$`, 700},
	} {
		p, err := compileEveryWay(c.pattern)
		if err != nil {
			t.Fatalf("pattern %q: %v", c.pattern, err)
		}
		table := p.table
		if p.backward != nil {
			table = p.backward.table
		}
		if table == nil || len(table) > c.cells {
			t.Errorf("pattern %q: table of %d cells, want at most %d", c.pattern, len(table), c.cells)
			continue
		}
		re := regexp.MustCompile(c.pattern)
		for _, s := range []string{"", "a", "john.smith@example.com", "@", "a@", "@a", "a@@a", "lorem ipsum dolor sit amet", "lorem  ipsum", "a,b,,c", "aab ab",
			strings.Repeat("ab ", 120), strings.Repeat("a", 70) + "@b", strings.Repeat("word ", 301), strings.Repeat("a", 31) + "b"} {
			checkPattern(t, p, c.pattern, s, re.MatchString(s))
		}
	}
}

func TestPatternsReadBackward(t *testing.T) {
	for _, source := range []string{`[ab]*a[ab]{14}$`, `-[0-9a-f-]{16}$`, `[^,]*,.{20}$`, `a.{14}`, `é\p{L}{14}$`, `\.(?:png|jpg|gif)$`, `[0-9]{4}-[0-9]{2}$`,
		`x.{1000}y.{1000}$`, `a|b$`} {
		p, err := compileEveryWay(source)
		if err != nil || p.table != nil || p.backward == nil {
			t.Fatalf("pattern %q: error %v, table %v, backward %v", source, err, p.table != nil, p.backward != nil)
		}
		re := regexp.MustCompile(source)
		for _, s := range []string{"", "a", "b", "ab", "ba", strings.Repeat("b", 15), "a" + strings.Repeat("b", 14), "xa" + strings.Repeat("ab", 7), "a" + strings.Repeat("b", 13) + "c",
			"-0123456789abcdef", "x-0123-456789abcde", "a,b," + strings.Repeat("c", 20), "," + strings.Repeat(",", 20), "\xffa" + strings.Repeat("\xc3", 14),
			strings.Repeat("é", 15), "aé" + strings.Repeat("ée", 7), "é" + strings.Repeat("e", 13) + "1", "\na" + strings.Repeat("😀", 14),
			"photo.jpg", "photo.jpeg", ".gif", "a.png ", "2026-11", "x2026-11", "2026-1", "2026-11-10",
			"x" + strings.Repeat("a", 1000) + "y" + strings.Repeat("b", 1000), "x" + strings.Repeat("a", 1000) + "y" + strings.Repeat("b", 999)} {
			checkPattern(t, p, source, s, re.MatchString(s))
		}
	}
	for _, source := range []string{`^a$`, `^[0-9]+$`, `a$|b`, `a\$`, `[$]`, `^a|b$`, `(a+$)`, `[ab]\\$`} {
		if p, err := Compile(source); err != nil || p.backward != nil {
			t.Errorf("pattern %q is read backward: error %v", source, err)
		}
	}
}

func TestPatternsWithoutTables(t *testing.T) {
	inputs := []string{"", "a", "ab", "a" + strings.Repeat("b", 15), "a" + strings.Repeat("a", 14) + "b", "xa" + strings.Repeat("ab", 7) + "b", "a" + strings.Repeat("b", 13) + "cb",
		"john.smith@example.com", "@", "a@", "@a", "a@@a", "é@😀", "\xff@\xff", strings.Repeat("u", 255) + "@" + strings.Repeat("d", 255), strings.Repeat("u", 256) + "@d",
		"u@" + strings.Repeat("d", 256), strings.Repeat("@", 300), strings.Repeat("word ", 300), strings.Repeat("word ", 299) + "word", strings.Repeat("a ", 299),
		strings.Repeat("w", 300), strings.Repeat("word ", 300) + "!", strings.Repeat("word  ", 300), "the quick brown fox",
		strings.Repeat("a", 1000) + "x" + strings.Repeat("b", 1000), strings.Repeat("a", 999) + "x" + strings.Repeat("b", 1000), strings.Repeat("a", 3000),
		strings.Repeat("a", 1500) + "x" + strings.Repeat("b", 999) + "x" + strings.Repeat("é", 1000), strings.Repeat("x", 2001), strings.Repeat("x", 2000),
		"abc", "a\nbc", strings.Repeat("xyz", 30) + "a" + strings.Repeat("xyz", 30) + "b" + strings.Repeat("xyz", 30) + "c", "a" + strings.Repeat("x", 501) + "bc",
		"a" + strings.Repeat("x", 500) + "b" + strings.Repeat("x", 500) + "c", "ca" + strings.Repeat("b", 600) + "c", "\na" + "bc"}
	if testing.Short() {
		inputs = slices.DeleteFunc(inputs, func(s string) bool { return len(s) > 300 })
	}
	for _, source := range []string{`a[ab]{14}b`, `^.{1,255}@.{1,255}$`, `^(\w+ ?){300}$`, `.{1000}x.{1000}`, `^.*a.{0,500}b.{0,500}c`, `^(?:(?:ab|a)+ ?){200}$`,
		`(?:^b|a)[ab]{14}b`, `a[ab]{14}(?:b|$)`, `a[ab]{7}\B[ab]{7}b`} {
		p, err := compileEveryWay(source)
		if err != nil || p.table != nil || p.backward != nil {
			t.Fatalf("pattern %q: error %v, table %v, backward %v", source, err, p.table != nil, p.backward != nil)
		}
		re := regexp.MustCompile(source)
		for _, s := range inputs {
			checkPattern(t, p, source, s, re.MatchString(s))
		}
	}
	bounded, _ := Compile(`^(\w+ ?){300}$`)
	needs, _ := Compile(`.{1000}x.{1000}`)
	if bounded.least != 300 || bounded.required != -1 || needs.least != 2001 || needs.required != 'x' {
		t.Errorf("least %d and %d, required %q and %q", bounded.least, needs.least, bounded.required, needs.required)
	}
}

func TestLiteralPatterns(t *testing.T) {
	inputs := []string{"", "error", "an error occurs", "erro", "errorerror", "https://example.com", "http://example.com", "see https://example.com", "data.json", "data.json ",
		".json", "exact", "exact ", " exact", "a.b", "axb", "é😀", "xé😀x", "\xc3\xa9😀", "\xc3é😀", "é\xf0\x9f\x98", "ab", "b", "\xff", "\xef\xbf\xbd"}
	for _, source := range []string{`error`, `^https://`, `\.json$`, `^exact$`, `^$`, ``, `^`, `$`, `a\.b`, `[a]b`, `é\u{1F600}`, `^^a`} {
		p, err := compileEveryWay(source)
		if err != nil || !p.literal {
			t.Fatalf("pattern %q: error %v", source, err)
		}
		re := regexp.MustCompile(strings.ReplaceAll(source, `\u{1F600}`, "😀"))
		for _, s := range inputs {
			checkPattern(t, p, source, s, re.MatchString(s))
		}
	}
	for _, source := range []string{`a$b`, `a^b`, `a|b`, `ab?`, `a.`, `\u{FFFD}`, `\u{D800}`, `a\b`} {
		p, err := compileEveryWay(source)
		if err != nil || p.literal {
			t.Fatalf("pattern %q: error %v", source, err)
		}
		for _, s := range inputs {
			checkPattern(t, p, source, s, p.forward.simulate(s))
		}
	}
}

func TestPatternTables(t *testing.T) {
	letters, err := Compile(`^\p{L}{2,50}$`)
	if err != nil || letters.width != 3 {
		t.Fatalf("letters and other runes should be the only two classes: error %v, width %d", err, letters.width)
	}
	anchored, _ := Compile(`^[a-z]+$`)
	if to := anchored.table[firstRow*int(anchored.width)+anchored.class('1')]; to != deadRow {
		t.Errorf("an anchored pattern keeps reading after a mismatch: row %d", to)
	}
	unanchored, _ := Compile(`[0-9]`)
	matched := unanchored.table[firstRow*int(unanchored.width)+unanchored.class('1')]
	if to := unanchored.table[int(matched)+unanchored.class('x')]; to != acceptedRow*unanchored.width {
		t.Errorf("an unanchored pattern keeps reading after a match: row %d", to)
	}
}

func contains(set []runeRange, r rune) bool {
	return slices.ContainsFunc(set, func(x runeRange) bool { return x.lo <= r && r <= x.hi })
}

var countWithLeadingZero = regexp.MustCompile(`[{,]0\d`)

func sameInGo(source, s string) bool {
	if strings.Contains(source, "[]") || strings.Contains(source, "[^]") || strings.Contains(source, "[:") ||
		countWithLeadingZero.MatchString(source) {
		return false
	}
	for i := 0; i < len(source); i++ {
		if source[i] == '\\' {
			i++
			if !strings.ContainsRune(`dDwWsSbBtnrfvx0`+syntaxCharacters, rune(source[i])) {
				return false
			}
		}
	}
	for _, r := range s {
		if contains(spaceRunes, r) != strings.ContainsRune("\t\n\f\r ", r) || r == '\r' {
			return false
		}
	}
	return true
}

func FuzzPattern(f *testing.F) {
	for _, c := range ecmaScriptPatterns {
		for _, s := range slices.Concat(c.accepts, c.rejects) {
			f.Add(c.pattern, s)
		}
	}
	for _, source := range ambiguous {
		f.Add(source, "aab ab")
	}
	f.Fuzz(func(t *testing.T, source, s string) {
		p, err := compileEveryWay(source)
		if err != nil {
			return
		}
		got := p.Match(s)
		checkPattern(t, p, source, s, got)
		if re, err := regexp.Compile(source); err == nil && sameInGo(source, s) && re.MatchString(s) != got {
			t.Fatalf("pattern %q, string %q: got %v, regexp says %v", source, s, got, !got)
		}
	})
}

var ecmaScriptPatterns = []struct {
	pattern          string
	accepts, rejects []string
}{
	{"", []string{"", "a"}, nil},
	{`es`, []string{"expression"}, []string{"e", "", "se"}},
	{`^es`, []string{"es", "ess"}, []string{"yes"}},
	{`es$`, []string{"es", "yes"}, []string{"ess"}},
	{`^$`, []string{""}, []string{"a", "\n"}},
	{`^a$`, []string{"a"}, []string{"a\n", "\na", "aa", ""}},
	{`a|b`, []string{"a", "b", "xbx"}, []string{"c"}},
	{`^a|b$`, []string{"ax", "xb"}, []string{"xbx", "xa"}},
	{`^(a|b)$`, []string{"a", "b"}, []string{"ab", ""}},
	{`^(?:ab)+$`, []string{"ab", "abab"}, []string{"aba", ""}},
	{`^(a|)$`, []string{"a", ""}, []string{"b"}},
	{`^()$`, []string{""}, []string{"a"}},
	{`a||b`, []string{"", "c"}, nil},
	{`^a*$`, []string{"", "a", "aaa"}, []string{"ab"}},
	{`^a+$`, []string{"a", "aaa"}, []string{"", "ab"}},
	{`^a?$`, []string{"", "a"}, []string{"aa"}},
	{`^a{2}$`, []string{"aa"}, []string{"a", "aaa"}},
	{`^a{2,}$`, []string{"aa", "aaaaa"}, []string{"a"}},
	{`^a{2,3}$`, []string{"aa", "aaa"}, []string{"a", "aaaa"}},
	{`^a{0}$`, []string{""}, []string{"a"}},
	{`^a{0,1}$`, []string{"", "a"}, []string{"aa"}},
	{`^a+?$`, []string{"a", "aaa"}, []string{""}},
	{`^a*?b$`, []string{"b", "aab"}, []string{"aa"}},
	{`^a{2,3}?$`, []string{"aa", "aaa"}, []string{"a", "aaaa"}},
	{`^a??$`, []string{"", "a"}, []string{"aa"}},
	{`^(ab){2}$`, []string{"abab"}, []string{"ab", "ababab"}},
	{`^(a|b){2,3}c$`, []string{"abc", "bbac"}, []string{"ac", "abbac"}},
	{`^(a*)*$`, []string{"", "aaa"}, []string{"ab"}},
	{`^(a+)+$`, []string{"aaa"}, []string{"aab", ""}},
	{`^(a|aa)+$`, []string{"a", "aaa"}, []string{"ab"}},
	{`^(a?){3}b$`, []string{"b", "ab", "aaab"}, []string{"aaaab"}},
	{`^[abc]$`, []string{"a", "c"}, []string{"d", "ab"}},
	{`^[a-c]$`, []string{"b"}, []string{"d", "-"}},
	{`^[^a-c]$`, []string{"d", "\n", "é"}, []string{"b", ""}},
	{`^[a\-c]$`, []string{"a", "-"}, []string{"b"}},
	{`^[-a]$`, []string{"-", "a"}, []string{"b"}},
	{`^[a-]$`, []string{"-", "a"}, []string{"b"}},
	{`^[a-b-c]$`, []string{"a", "b", "-", "c"}, []string{"d"}},
	{`^[+--]$`, []string{"+", ",", "-"}, []string{"."}},
	{`^[\]]$`, []string{"]"}, []string{"["}},
	{`^[\\]$`, []string{"\\"}, []string{"a"}},
	{`^[[]$`, []string{"["}, []string{"]"}},
	{`^[^]$`, []string{"a", "\n"}, []string{"", "ab"}},
	{`^[]$`, nil, []string{"", "a"}},
	{`^a[]?$`, []string{"a"}, []string{"ab"}},
	{`^[\d\s]$`, []string{"1", " "}, []string{"a"}},
	{`^[^\d]$`, []string{"a"}, []string{"1"}},
	{`^[^\D]$`, []string{"1"}, []string{"a"}},
	{`^[\b]$`, []string{"\b"}, []string{"b"}},
	{`^[\w-]$`, []string{"a", "-", "_"}, []string{"."}},
	{`^[.]$`, []string{"."}, []string{"a"}},
	{`^[*+?(){}|]$`, []string{"*", "{", "|"}, []string{"a"}},
	{`^[$^]$`, []string{"$", "^"}, []string{"a"}},
	{`^[a^]$`, []string{"^", "a"}, []string{"b"}},
	{`^[a-zA-Z0-9_.+-]+$`, []string{"john.smith+tag"}, []string{"john smith"}},
	{`^[\u0041-\u005A]$`, []string{"A", "Z"}, []string{"a"}},
	{`^[é-ü]$`, []string{"é", "ü"}, []string{"e", "ÿ"}},
	{`^[\u{1F600}-\u{1F60E}]$`, []string{"😀", "😎"}, []string{"😏"}},
	{`^[\S\s]$`, []string{"a", " ", "\n"}, nil},
	{`^[\W\d]$`, []string{"1", "-"}, []string{"a"}},
	{`^\d$`, []string{"0", "9"}, []string{"a", "٣", "１"}},
	{`^\D$`, []string{"a", "٣"}, []string{"0"}},
	{`^\w$`, []string{"a", "Z", "0", "_"}, []string{"-", "é"}},
	{`^\W$`, []string{"-", "é"}, []string{"a"}},
	{`^\s$`, []string{" ", "\t", "\n", "\r", "\v", "\f", "\u00a0", "\u1680", "\u2003", "\u2028", "\u2029", "\u202f", "\u205f", "\u3000", "\ufeff"}, []string{"a", "\u200b", "\u0085"}},
	{`^\S$`, []string{"a", "\u200b"}, []string{" ", "\u00a0"}},
	{`^\t\n\r\f\v$`, []string{"\t\n\r\f\v"}, []string{"tnrfv"}},
	{`^\0$`, []string{"\x00"}, []string{"0"}},
	{`^\x41\x7a$`, []string{"Az"}, []string{"x41"}},
	{`^\u0041\u00e9$`, []string{"Aé"}, []string{"A"}},
	{`^\u{41}\u{e9}\u{1F600}$`, []string{"Aé😀"}, []string{"A"}},
	{`^\uD83D\uDE00$`, []string{"😀"}, []string{"x"}},
	{`^[\uD83D\uDE00-\uD83D\uDE0E]$`, []string{"😀", "😎"}, []string{"😏"}},
	{`^\cJ\cj\cA$`, []string{"\n\n\x01"}, []string{"J"}},
	{`^\/\.\*\+\?\(\)\[\]\{\}\|\^\$\\$`, []string{"/.*+?()[]{}|^$\\"}, []string{"/"}},
	{`^.$`, []string{"a", "\u0085", "😀", "é"}, []string{"\n", "\r", "\u2028", "\u2029", "", "ab"}},
	{`^.*$`, []string{"", "abc"}, []string{"a\nb"}},
	{`a.c`, []string{"abc", "xxa-cxx"}, []string{"a\nc", "ac"}},
	{`\bfoo\b`, []string{"foo", "a foo b", "foo-bar", "-foo-"}, []string{"foobar", "xfoo", "foo_"}},
	{`\Bfoo`, []string{"xfoo"}, []string{"foo", " foo"}},
	{`foo\B`, []string{"foox"}, []string{"foo", "foo "}},
	{`^\b`, []string{"a"}, []string{"", " a", "-"}},
	{`\b$`, []string{"a"}, []string{"", "a ", "-"}},
	{`^\B$`, []string{""}, []string{"a"}},
	{`\b\b`, []string{"a"}, []string{"", "-"}},
	{`a\b-`, []string{"a-"}, []string{"ab-"}},
	{`\bé`, []string{"aé"}, []string{"é", " é"}},
	{`^(\b|-)a`, []string{"a", "-a"}, []string{" a"}},
	{`^\p{L}+$`, []string{"Élodie", "abc", "日本語"}, []string{"ab1", ""}},
	{`^\P{L}+$`, []string{"123"}, []string{"1a"}},
	{`^\p{Lu}\p{Ll}+$`, []string{"Élodie"}, []string{"élodie", "ÉLODIE"}},
	{`^\p{Letter}$`, []string{"a"}, []string{"1"}},
	{`^\p{Uppercase_Letter}$`, []string{"A"}, []string{"a"}},
	{`^\p{gc=L}$`, []string{"a"}, []string{"1"}},
	{`^\p{General_Category=Decimal_Number}$`, []string{"1", "٣"}, []string{"a"}},
	{`^\p{Nd}$`, []string{"1", "٣"}, []string{"a", "½"}},
	{`^\p{N}$`, []string{"1", "½"}, []string{"a"}},
	{`^\p{digit}$`, []string{"1"}, []string{"a"}},
	{`^\p{punct}$`, []string{"!"}, []string{"a"}},
	{`^\p{P}$`, []string{"!", "-"}, []string{"a", "+"}},
	{`^\p{S}$`, []string{"+", "€"}, []string{"a"}},
	{`^\p{Zs}$`, []string{" ", "\u00a0"}, []string{"\t"}},
	{`^\p{Cc}$`, []string{"\t"}, []string{"a"}},
	{`^\p{LC}$`, []string{"a", "A"}, []string{"日"}},
	{`^\p{M}$`, []string{"́"}, []string{"a"}},
	{`^\p{Script=Greek}+$`, []string{"αβγ"}, []string{"abc"}},
	{`^\p{sc=Latin}+$`, []string{"abc", "Élodie"}, []string{"αβγ"}},
	{`^\p{Script=Han}+$`, []string{"日本"}, []string{"abc"}},
	{`^\P{Script=Latin}$`, []string{"α"}, []string{"a"}},
	{`^[\p{L}\d '-]+$`, []string{"Jean-Luc O'Neil 2"}, []string{"a_b"}},
	{`^[^\p{L}]$`, []string{"1"}, []string{"a"}},
	{`[0-9]+`, []string{"order 12345"}, []string{"no digits", ""}},
	{`^v\d`, []string{"v1.2"}, []string{"x1"}},
	{`\d$`, []string{"abc1"}, []string{"1abc"}},
	{`^\d{4}-\d{2}-\d{2}$`, []string{"2026-11-10"}, []string{"2026-11-1", "x2026-11-10"}},
	{`^[a-z0-9]+(-[a-z0-9]+)*$`, []string{"my-blog-post-42"}, []string{"my--blog", "-my", "my-", "My"}},
	{`^[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}$`, []string{"john.smith@example.com"}, []string{"john@", "@example.com", "a@b.c"}},
	{`^[^@\s]+@[^@\s]+$`, []string{"a@b"}, []string{"a b@c", "a@b@c", "a@\u00a0"}},
	{`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`, []string{"123e4567-e89b-12d3-a456-426614174000"}, []string{"123e4567-e89b-12d3-a456-42661417400", "123E4567-e89b-12d3-a456-426614174000"}},
	{`^v?\d+\.\d+\.\d+(-[0-9A-Za-z.-]+)?$`, []string{"v1.27.3-rc.1", "1.2.3"}, []string{"1.2", "v1.2.3-"}},
	{`^\+?[1-9][0-9]{7,14}$`, []string{"+33612345678", "33612345678"}, []string{"+0612345678", "+3361234567890123456"}},
	{`^#[0-9a-fA-F]{6}$`, []string{"#1a2B3c"}, []string{"#1a2B3", "#1a2B3cd"}},
	{`^(draft|published|archived)$`, []string{"draft", "published"}, []string{"publishe", "xdraft"}},
	{`^https?://[^\s/$.?#][^\s]*$`, []string{"https://example.com/a?b=c", "http://x"}, []string{"https://", "ftp://x", "https://a b"}},
	{`^(\d{1,3}\.){3}\d{1,3}$`, []string{"192.168.1.1"}, []string{"192.168.1", "1.2.3.4.5"}},
	{`^[A-Z]{2}\d{2}( ?[A-Z0-9]{4}){3,7}$`, []string{"FR76 3000 6000 0112 3456"}, []string{"FR7630006000011234567890189", "fr76"}},
}

var ecmaScriptSyntaxErrors = []string{
	`a**`, `*a`, `+`, `?`, `a{2,1}`, `a{`, `a{,3}`, `a{2`, `a{x}`, `a}`, `{`, `]`, `[a`, `(a`, `a)`, `(`, `\`, `\e`, `\z`, `\A`, `\Q`, `\-`, `\_`, `\a`,
	`\ `, `\é`, `\x4`, `\xZZ`, `\u00`, `\u{110000}`, `\u00zz`, `\u`, `\u{}`, `\u{41`, `\c1`, `\c`, `\00`, `\01`, `[z-a]`, `[\d-x]`, `[x-\d]`, `[\B]`,
	`[\e]`, `[a-z&&[^b]]`, `[[:alpha:]]`, `[]a]`, `(?i)a`, `(?P<n>a)`, `(?)`, `(?a)`, `\pL`, `\p`, `\p{Foo}`, `\p{Greek}`, `\p{letter}`, `\p{L`,
	`\p{gc=Foo}`, `\p{Script=Foo}`, `\p{foo=L}`, `^*`, `$+`, `\b+`, `\B?`, `a{1}{2}`, `a++`, `a*+`, `a???`, `\1`, `\k<n>`, `(a)\2`,
}

var unsupportedPatterns = []string{
	`a(?=b)`, `a(?!b)`, `(?<=a)b`, `(?<!a)b`, `(?<n>a)`, `(a)\1`, `(?<n>a)\k<n>`, `(?i:a)`, `\p{Emoji}`, `\p{Script=Grek}`, `\p{scx=Greek}`,
	`\p{Script_Extensions=Greek}`, `\p{Any}`, `\p{ASCII}`, `\p{Alphabetic}`, `a{1001}`, `(a{1000}){1000}`,
}
