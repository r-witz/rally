// Package pattern matches strings against ECMAScript regular expressions in
// Unicode mode, the dialect of JSON Schema, in time linear in the string.
package pattern

import (
	"cmp"
	"encoding/binary"
	"errors"
	"fmt"
	"math/bits"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"
)

const (
	maxPatternRepeat = 1000
	maxPatternInsts  = 1 << 12
	maxPatternTable  = 1 << 15
	maxBackwardTable = 1 << 12
)

type runeRange struct{ lo, hi rune }

var (
	digitRunes      = []runeRange{{'0', '9'}}
	wordRunes       = []runeRange{{'0', '9'}, {'A', 'Z'}, {'_', '_'}, {'a', 'z'}}
	lineTerminators = []runeRange{{'\n', '\n'}, {'\r', '\r'}, {0x2028, 0x2029}}
	spaceRunes      = []runeRange{{'\t', '\r'}, {' ', ' '}, {0xa0, 0xa0}, {0x1680, 0x1680}, {0x2000, 0x200a},
		{0x2028, 0x2029}, {0x202f, 0x202f}, {0x205f, 0x205f}, {0x3000, 0x3000}, {0xfeff, 0xfeff}}

	anyButLineTerminator = negated(lineTerminators)
	classEscapes         = map[rune][]runeRange{
		'd': digitRunes, 'D': negated(digitRunes),
		's': spaceRunes, 'S': negated(spaceRunes),
		'w': wordRunes, 'W': negated(wordRunes),
	}
	controlEscapes = map[rune]rune{'f': '\f', 'n': '\n', 'r': '\r', 't': '\t', 'v': '\v'}
)

const syntaxCharacters = `^$\.*+?()[]{}|/`

func merged(set []runeRange) []runeRange {
	slices.SortFunc(set, func(a, b runeRange) int { return cmp.Compare(a.lo, b.lo) })
	out := set[:0]
	for _, r := range set {
		if n := len(out); n > 0 && r.lo <= out[n-1].hi+1 {
			out[n-1].hi = max(out[n-1].hi, r.hi)
		} else {
			out = append(out, r)
		}
	}
	return out
}

func negated(set []runeRange) []runeRange {
	var out []runeRange
	next := rune(0)
	for _, r := range set {
		if r.lo > next {
			out = append(out, runeRange{next, r.lo - 1})
		}
		next = r.hi + 1
	}
	if next <= unicode.MaxRune {
		out = append(out, runeRange{next, unicode.MaxRune})
	}
	return out
}

type assertion uint8

const (
	assertBegin assertion = iota
	assertEnd
	assertWordBoundary
	assertNotWordBoundary
)

type position uint8

const (
	atBegin position = 1 << iota
	atEnd
	afterWord
	beforeWord
)

func (a assertion) holds(at position) bool {
	switch a {
	case assertBegin:
		return at&atBegin != 0
	case assertEnd:
		return at&atEnd != 0
	}
	boundary := (at&afterWord != 0) != (at&beforeWord != 0)
	return boundary == (a == assertWordBoundary)
}

type instOp uint8

const (
	instMatch instOp = iota
	instSet
	instAssert
	instSplit
)

type patternInst struct {
	op        instOp
	chain     int32
	set       int
	assertion assertion
	out, alt  int
}

type chain struct{ base, step, next, parent, slot int }

// Pattern is a compiled regular expression.
type Pattern struct {
	sets       [][]runeRange
	boundaries bool
	insts      []patternInst
	start      int

	bounds  []rune
	classes []int
	members [][]byte
	ascii   [utf8.RuneSelf]uint16
	table   []uint16
	width   uint16
	chains  []chain
	slots   int

	backward *Pattern

	literal  bool
	text     string
	anchors  position
	least    int
	required rune
}

type patternError string

type patternParser struct {
	*Pattern
	src      string
	pos      int
	reversed bool
}

type fragment func(next int) int

// Compile parses src and builds what Match needs. Lookaround, backreferences,
// named groups and modifiers are not supported.
func Compile(src string) (p *Pattern, err error) {
	defer func() {
		if r := recover(); r != nil {
			reason, ok := r.(patternError)
			if !ok {
				panic(r)
			}
			p, err = nil, errors.New(string(reason))
		}
	}()
	endOnly := strings.HasSuffix(src, "$") && !strings.HasSuffix(src, `\$`) && !strings.HasPrefix(src, "^")
	p = newPattern(src, endOnly)
	if p.spell(endOnly) {
		return p, nil
	}
	if endOnly {
		if p.fits(maxPatternTable) {
			return &Pattern{backward: p}, nil
		}
		p = newPattern(src, false)
	}
	if !p.fits(maxPatternTable) {
		if !endOnly {
			p.readBackward(src, maxBackwardTable)
		}
		if p.backward == nil {
			p.summarize()
		}
	}
	return p, nil
}

func (p *Pattern) fits(cells int) bool {
	p.classify()
	p.tabulate(cells)
	return p.table != nil
}

func (p *Pattern) readBackward(src string, cells int) {
	if backward := newPattern(src, true); backward.fits(cells) {
		p.backward = backward
	}
}

func newPattern(src string, reversed bool) *Pattern {
	p := &Pattern{sets: make([][]runeRange, 0, len(src)+1), insts: make([]patternInst, 0, 2*len(src)+1)}
	p.add(patternInst{op: instMatch})
	parser := patternParser{Pattern: p, src: src, reversed: reversed}
	whole := parser.disjunction()
	if parser.pos < len(src) {
		parser.fail("unmatched )")
	}
	if p.boundaries {
		p.sets = append(p.sets, wordRunes)
	}
	p.start = whole(0)
	return p
}

func (p *Pattern) asserts(pc int, a assertion) bool {
	return p.insts[pc].op == instAssert && p.insts[pc].assertion == a
}

func (p *Pattern) only(set int) (r rune, single bool) {
	ranges := p.sets[set]
	if len(ranges) != 1 || ranges[0].lo != ranges[0].hi {
		return 0, false
	}
	return ranges[0].lo, true
}

func (p *Pattern) spell(reversed bool) bool {
	var text []rune
	var anchors position
	for pc := p.start; pc != 0; pc = p.insts[pc].out {
		r, single := rune(0), false
		if p.insts[pc].op == instSet {
			r, single = p.only(p.insts[pc].set)
		}
		switch {
		case p.asserts(pc, assertBegin) && len(text) == 0:
			anchors |= atBegin
		case p.asserts(pc, assertEnd) && p.insts[pc].out == 0:
			anchors |= atEnd
		case single && utf8.ValidRune(r) && r != utf8.RuneError:
			text = append(text, r)
		default:
			return false
		}
	}
	if reversed {
		slices.Reverse(text)
		anchors = anchors&atBegin<<1 | anchors&atEnd>>1
	}
	p.literal, p.text, p.anchors = true, string(text), anchors
	return true
}

func (p *Pattern) summarize() {
	least, required := make([]int, len(p.insts)), make([]int, len(p.insts))
	for pc := 1; pc < len(p.insts); pc++ {
		inst := &p.insts[pc]
		switch {
		case inst.op != instSplit:
			least[pc], required[pc] = least[inst.out], required[inst.out]
		case inst.out >= pc:
			least[pc], required[pc] = least[inst.alt], required[inst.alt]
		default:
			a, b := required[inst.out], required[inst.alt]
			for a != b {
				if a < b {
					a, b = b, a
				}
				a = required[p.insts[a].out]
			}
			least[pc], required[pc] = min(least[inst.out], least[inst.alt]), a
		}
		if inst.op == instSet {
			least[pc]++
			if _, single := p.only(inst.set); single {
				required[pc] = pc
			}
		}
	}
	p.least, p.required = least[p.start], -1
	if pc := required[p.start]; pc != 0 {
		p.required, _ = p.only(p.insts[pc].set)
	}
}

func (p *patternParser) fail(format string, args ...any) {
	panic(patternError(fmt.Sprintf(format, args...)))
}

func (p *patternParser) eat(token string) bool {
	if !strings.HasPrefix(p.src[p.pos:], token) {
		return false
	}
	p.pos += len(token)
	return true
}

func (p *patternParser) next() rune {
	if p.pos == len(p.src) {
		p.fail("unexpected end")
	}
	r, size := utf8.DecodeRuneInString(p.src[p.pos:])
	p.pos += size
	return r
}

func (p *patternParser) take(n int) string {
	if len(p.src)-p.pos < n {
		p.fail("unexpected end")
	}
	p.pos += n
	return p.src[p.pos-n : p.pos]
}

func (p *patternParser) until(end byte) string {
	n := strings.IndexByte(p.src[p.pos:], end)
	if n < 0 {
		p.fail("missing %c", end)
	}
	return p.take(n + 1)[:n]
}

func (p *patternParser) digitAhead() bool {
	return p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9'
}

func (p *patternParser) one(inst patternInst) fragment {
	return func(next int) int {
		inst.out = next
		return p.add(inst)
	}
}

func (p *patternParser) assert(a assertion) fragment {
	p.boundaries = p.boundaries || a == assertWordBoundary || a == assertNotWordBoundary
	return p.one(patternInst{op: instAssert, assertion: a})
}

func (p *patternParser) disjunction() fragment {
	alternatives := []fragment{p.alternative()}
	for p.eat("|") {
		alternatives = append(alternatives, p.alternative())
	}
	return func(next int) int {
		entry := alternatives[len(alternatives)-1](next)
		for _, alternative := range slices.Backward(alternatives[:len(alternatives)-1]) {
			entry = p.add(patternInst{op: instSplit, out: alternative(next), alt: entry})
		}
		return entry
	}
}

func (p *patternParser) alternative() fragment {
	terms := make([]fragment, 0, 8)
	for p.pos < len(p.src) && p.src[p.pos] != '|' && p.src[p.pos] != ')' {
		terms = append(terms, p.term())
	}
	if p.reversed {
		slices.Reverse(terms)
	}
	return func(next int) int {
		for _, term := range slices.Backward(terms) {
			next = term(next)
		}
		return next
	}
}

func (p *patternParser) term() fragment {
	var set []runeRange
	switch r := p.next(); r {
	case '^', '$':
		if (r == '^') != p.reversed {
			return p.assert(assertBegin)
		}
		return p.assert(assertEnd)
	case '(':
		return p.quantified(p.group())
	case '[':
		set = p.class()
	case '.':
		set = anyButLineTerminator
	case '\\':
		switch {
		case p.eat("b"):
			return p.assert(assertWordBoundary)
		case p.eat("B"):
			return p.assert(assertNotWordBoundary)
		}
		set, _ = p.escape(false)
	case '*', '+', '?':
		p.fail("nothing to repeat before %c", r)
	case '{', '}', ']':
		p.fail("%c must be escaped", r)
	default:
		set = []runeRange{{r, r}}
	}
	p.sets = append(p.sets, set)
	return p.quantified(p.one(patternInst{op: instSet, set: len(p.sets) - 1}))
}

func (p *patternParser) group() fragment {
	if p.eat("?") && !p.eat(":") {
		p.fail("(?: is the only supported group starting with (?")
	}
	group := p.disjunction()
	if !p.eat(")") {
		p.fail("missing )")
	}
	return group
}

func (p *patternParser) quantified(atom fragment) fragment {
	least, most := 1, 1
	switch {
	case p.eat("*"):
		least, most = 0, -1
	case p.eat("+"):
		most = -1
	case p.eat("?"):
		least = 0
	case p.eat("{"):
		least = p.count()
		most = least
		if p.eat(",") {
			most = -1
			if p.digitAhead() {
				most = p.count()
			}
		}
		if !p.eat("}") || most >= 0 && most < least {
			p.fail("invalid {} quantifier")
		}
	default:
		return atom
	}
	p.eat("?")
	return func(next int) int {
		entry, rest := next, least
		if most < 0 {
			entry = p.add(patternInst{op: instSplit, alt: next})
			body := atom(entry)
			p.insts[entry].out = body
			if least > 0 {
				entry, rest = body, least-1
			}
		}
		first, base := len(p.chains), len(p.insts)
		for range most - least {
			out := atom(entry)
			if out == entry {
				break
			}
			entry = p.add(patternInst{op: instSplit, out: out, alt: next})
		}
		if most-least > 1 && len(p.insts) > base {
			p.chain(first, base, most-least, next)
		}
		for range rest {
			out := atom(entry)
			if out == entry {
				break
			}
			entry = out
		}
		return entry
	}
}

func (p *patternParser) count() int {
	start := p.pos
	for p.digitAhead() {
		p.pos++
	}
	n, err := strconv.Atoi(p.src[start:p.pos])
	if err != nil || n > maxPatternRepeat {
		p.fail("a repetition count must be a number up to %d", maxPatternRepeat)
	}
	return n
}

func (p *patternParser) class() []runeRange {
	negate := p.eat("^")
	var set []runeRange
	for !p.eat("]") {
		atom, single := p.classAtom()
		if p.pos+1 < len(p.src) && p.src[p.pos] == '-' && p.src[p.pos+1] != ']' {
			p.pos++
			last, lastSingle := p.classAtom()
			if !single || !lastSingle || last[0].lo < atom[0].lo {
				p.fail("invalid range in a character class")
			}
			atom = []runeRange{{atom[0].lo, last[0].lo}}
		}
		set = append(set, atom...)
	}
	set = merged(set)
	if negate {
		return negated(set)
	}
	return set
}

func (p *patternParser) classAtom() (set []runeRange, single bool) {
	if r := p.next(); r != '\\' {
		return []runeRange{{r, r}}, true
	}
	return p.escape(true)
}

func (p *patternParser) escape(inClass bool) (set []runeRange, single bool) {
	r := p.next()
	if set, ok := classEscapes[r]; ok {
		return set, false
	}
	switch control, isControl := controlEscapes[r]; {
	case isControl:
		r = control
	case r == 'p':
		return p.property(), false
	case r == 'P':
		return negated(p.property()), false
	case r == 'c':
		if r = p.next() &^ 0x20; r < 'A' || r > 'Z' {
			p.fail(`\c must be followed by a letter`)
		}
		r -= 'A' - 1
	case r == '0' && !p.digitAhead():
		r = 0
	case r == 'x':
		r = p.hex(p.take(2))
	case r == 'u':
		r = p.unicodeEscape()
	case r == 'b' && inClass:
		r = '\b'
	case r == '-' && inClass:
	case r >= '1' && r <= '9', r == 'k':
		p.fail("backreferences are not supported")
	case !strings.ContainsRune(syntaxCharacters, r):
		p.fail(`invalid escape \%c`, r)
	}
	return []runeRange{{r, r}}, true
}

func (p *patternParser) hex(digits string) rune {
	v, err := strconv.ParseUint(digits, 16, 32)
	if err != nil || v > unicode.MaxRune {
		p.fail("invalid hexadecimal escape %q", digits)
	}
	return rune(v)
}

func (p *patternParser) unicodeEscape() rune {
	if p.eat("{") {
		return p.hex(p.until('}'))
	}
	r := p.hex(p.take(4))
	if rest := p.src[p.pos:]; utf16.IsSurrogate(r) && len(rest) >= 6 && rest[:2] == `\u` {
		if low, err := strconv.ParseUint(rest[2:6], 16, 32); err == nil {
			if pair := utf16.DecodeRune(r, rune(low)); pair != unicode.ReplacementChar {
				p.pos += 6
				return pair
			}
		}
	}
	return r
}

func (p *patternParser) property() []runeRange {
	if !p.eat("{") {
		p.fail(`\p must be followed by {`)
	}
	name := p.until('}')
	key, value, found := strings.Cut(name, "=")
	if !found {
		key, value = "gc", name
	}
	var table *unicode.RangeTable
	switch key {
	case "gc", "General_Category":
		if short, ok := unicode.CategoryAliases[value]; ok {
			value = short
		}
		table = unicode.Categories[value]
	case "sc", "Script":
		table = unicode.Scripts[value]
	}
	if table == nil {
		p.fail("unsupported Unicode property %s", name)
	}
	var set []runeRange
	add := func(lo, hi, stride rune) {
		if stride == 1 {
			set = append(set, runeRange{lo, hi})
			return
		}
		for r := lo; r <= hi; r += stride {
			set = append(set, runeRange{r, r})
		}
	}
	for _, r := range table.R16 {
		add(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	for _, r := range table.R32 {
		add(rune(r.Lo), rune(r.Hi), rune(r.Stride))
	}
	return set
}

func (p *Pattern) add(inst patternInst) int {
	if len(p.insts) == maxPatternInsts {
		panic(patternError("the pattern is too large"))
	}
	p.insts = append(p.insts, inst)
	return len(p.insts) - 1
}

func (p *Pattern) chain(first, base, copies, next int) {
	id, step := len(p.chains)+1, (len(p.insts)-base)/copies
	for i := first; i < len(p.chains); i++ {
		if p.chains[i].parent == 0 {
			p.chains[i].parent = id
		}
	}
	for pc := base; pc < len(p.insts); pc++ {
		if p.insts[pc].chain == 0 {
			p.insts[pc].chain = int32(id)
		}
	}
	p.chains = append(p.chains, chain{base: base, step: step, next: next, slot: p.slots})
	p.slots += step
}

func (p *Pattern) classify() {
	p.bounds = make([]rune, 1, 2*len(p.sets)+1)
	for _, set := range p.sets {
		for _, r := range set {
			p.bounds = append(p.bounds, r.lo, r.hi+1)
		}
	}
	slices.Sort(p.bounds)
	p.bounds = slices.Compact(p.bounds)
	p.classes = make([]int, len(p.bounds))
	known := map[string]int{}
	members, cursors := make([]byte, len(p.sets)), make([]int, len(p.sets))
	for i, lo := range p.bounds {
		for j, set := range p.sets {
			for cursors[j] < len(set) && set[cursors[j]].hi < lo {
				cursors[j]++
			}
			members[j] = 0
			if cursors[j] < len(set) && set[cursors[j]].lo <= lo {
				members[j] = 1
			}
		}
		class, ok := known[string(members)]
		if !ok {
			class = len(p.members)
			known[string(members)] = class
			p.members = append(p.members, slices.Clone(members))
		}
		p.classes[i] = class
		for b := lo; b < utf8.RuneSelf && (i+1 == len(p.bounds) || b < p.bounds[i+1]); b++ {
			p.ascii[b] = uint16(class)
		}
	}
}

func (p *Pattern) class(r rune) int {
	i, found := slices.BinarySearch(p.bounds, r)
	if !found {
		i--
	}
	return p.classes[i]
}

const (
	deadRow = iota
	acceptedRow
	firstRow
)

func (p *Pattern) tabulate(cells int) {
	width := len(p.members) + 1
	table := make([]uint16, firstRow*width, (firstRow+len(p.insts))*width)
	table[acceptedRow*width+width-1] = 1

	closed, next := p.kernel(), p.kernel()
	words := len(next)
	kernels := append(make([]uint64, firstRow*words), next...)
	positions := []position{firstRow: atBegin}
	rows := make(map[string]int, len(p.insts))
	marks, movers := make([]int, p.slots), 2
	if p.insts[p.start].chain != 0 || slices.ContainsFunc(p.chains, func(c chain) bool { return c.next == p.start }) {
		movers = 1
	}
	var restarts [afterWord + 1]int
	var key []byte
	for row := firstRow; row < len(positions); row++ {
		if len(positions)*width > cells {
			return
		}
		kernel, from := kernels[row*words:(row+1)*words], positions[row]
		closedAt := from | atEnd
		matched := p.closure(closed, kernel, closedAt)
		final := matched
		for class := range width - 1 {
			if at := from | p.wordAt(class, beforeWord); at != closedAt {
				closedAt, matched = at, p.closure(closed, kernel, at)
			}
			to := acceptedRow
			if !matched {
				moved := p.consume(next, closed, class)
				at := p.wordAt(class, afterWord)
				if to = restarts[at]; moved > 0 || to == 0 {
					if moved >= movers && p.slots > 0 {
						p.prune(next, marks, len(table))
					}
					key = append(key[:0], byte(at))
					for _, word := range next {
						key = binary.LittleEndian.AppendUint64(key, word)
					}
					known := false
					if to, known = rows[string(key)]; !known {
						to = len(positions)
						rows[string(key)] = to
						kernels, positions = append(kernels, next...), append(positions, at)
					}
					if moved == 0 {
						restarts[at] = to
					}
				}
			}
			table = append(table, uint16(to*width))
		}
		table = append(table, 0)
		if final {
			table[len(table)-1] = 1
		}
	}
	for row := firstRow; row < len(positions); row++ {
		cells, self := table[row*width:(row+1)*width], uint16(row*width)
		leaves := func(to uint16) bool { return to != self }
		if cells[width-1] == 0 && !slices.ContainsFunc(cells[:width-1], leaves) {
			for i, to := range table {
				if to == self {
					table[i] = deadRow
				}
			}
		}
	}
	p.table, p.width = table, uint16(width)
}

// Match reports whether s contains a match of the pattern.
func (p *Pattern) Match(s string) bool {
	if p.table == nil {
		return p.matchesWithoutTable(s)
	}
	row := firstRow * p.width
	for i := 0; i < len(s) && row > acceptedRow*p.width; {
		if b := s[i]; b < utf8.RuneSelf {
			row = p.table[row+p.ascii[b]]
			i++
		} else {
			r, size := utf8.DecodeRuneInString(s[i:])
			row = p.table[int(row)+p.class(r)]
			i += size
		}
	}
	return p.table[row+p.width-1] != 0
}

func (p *Pattern) matchesWithoutTable(s string) bool {
	switch {
	case p.backward != nil:
		return p.backward.matchesBackward(s)
	case p.literal && p.anchors == 0:
		return strings.Contains(s, p.text)
	case p.literal && p.anchors == atBegin:
		return strings.HasPrefix(s, p.text)
	case p.literal && p.anchors == atEnd:
		return strings.HasSuffix(s, p.text)
	case p.literal:
		return s == p.text
	case len(s) < p.least || p.required >= 0 && !strings.ContainsRune(s, p.required):
		return false
	}
	return p.simulate(s)
}

func (p *Pattern) matchesBackward(s string) bool {
	row := firstRow * p.width
	for i := len(s); i > 0 && row > acceptedRow*p.width; {
		if b := s[i-1]; b < utf8.RuneSelf {
			row = p.table[row+p.ascii[b]]
			i--
		} else {
			r, size := utf8.DecodeLastRuneInString(s[:i])
			row = p.table[int(row)+p.class(r)]
			i -= size
		}
	}
	return p.table[row+p.width-1] != 0
}

func (p *Pattern) simulate(s string) bool {
	kernel, closed, at := p.kernel(), p.kernel(), atBegin
	anchored := p.asserts(p.start, assertBegin)
	for _, r := range s {
		class := p.class(r)
		if p.closure(closed, kernel, at|p.wordAt(class, beforeWord)) {
			return true
		}
		if p.consume(kernel, closed, class) == 0 && anchored {
			return false
		}
		at = p.wordAt(class, afterWord)
	}
	return p.closure(closed, kernel, at|atEnd)
}

func (p *Pattern) kernel() []uint64 {
	kernel := make([]uint64, (len(p.insts)+63)/64)
	kernel[p.start/64] |= 1 << (p.start % 64)
	return kernel
}

func (p *Pattern) wordAt(class int, side position) position {
	if p.boundaries && p.members[class][len(p.sets)-1] != 0 {
		return side
	}
	return 0
}

func (p *Pattern) closure(closed, kernel []uint64, at position) (matched bool) {
	clear(closed)
	for i, word := range kernel {
		for ; word != 0; word &= word - 1 {
			p.follow(closed, i*64+bits.TrailingZeros64(word), at)
		}
	}
	return closed[0]&1 != 0
}

func (p *Pattern) follow(closed []uint64, pc int, at position) {
	if closed[pc/64]>>(pc%64)&1 != 0 {
		return
	}
	closed[pc/64] |= 1 << (pc % 64)
	switch inst := &p.insts[pc]; inst.op {
	case instSplit:
		p.follow(closed, inst.out, at)
		p.follow(closed, inst.alt, at)
	case instAssert:
		if inst.assertion.holds(at) {
			p.follow(closed, inst.out, at)
		}
	}
}

func (p *Pattern) prune(kernel []uint64, marks []int, mark int) {
	for i := len(kernel) - 1; i >= 0; i-- {
		for word := kernel[i]; word != 0; {
			pc, subsumed := i*64+bits.Len64(word)-1, false
			for id := int(p.insts[pc].chain); id != 0; {
				c := &p.chains[id-1]
				offset := (pc - c.base) % c.step
				if offset == c.step-1 {
					kernel[c.next/64] &^= 1 << (c.next % 64)
				}
				subsumed = subsumed || marks[c.slot+offset] == mark
				marks[c.slot+offset] = mark
				id = c.parent
			}
			if subsumed {
				kernel[i] &^= 1 << (pc % 64)
			}
			word = kernel[i] & (1<<(pc%64) - 1)
		}
	}
}

func (p *Pattern) consume(kernel, closed []uint64, class int) (moved int) {
	clear(kernel)
	kernel[p.start/64] |= 1 << (p.start % 64)
	for i, word := range closed {
		for ; word != 0; word &= word - 1 {
			inst := &p.insts[i*64+bits.TrailingZeros64(word)]
			if inst.op == instSet && p.members[class][inst.set] != 0 {
				kernel[inst.out/64] |= 1 << (inst.out % 64)
				moved++
			}
		}
	}
	return moved
}
