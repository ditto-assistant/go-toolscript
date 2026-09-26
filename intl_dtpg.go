package toolscript

import (
	"strings"
	"sync"
)

// A port of ICU4C's DateTimePatternGenerator (i18n/dtptngen.cpp, ICU 78,
// Unicode-3.0 license) restricted to what ECMA-402 skeletons need, loaded
// with the CLDR "en" Gregorian data. It turns a skeleton such as "yMMMdhm"
// into the locale's pattern ("MMM d, y, h:mm a"), including ICU's
// tie-breaking and append-item fallbacks, so output matches V8 exactly.

const (
	dtpgEra = iota
	dtpgYear
	dtpgQuarter
	dtpgMonth
	dtpgWeekOfYear
	dtpgWeekOfMonth
	dtpgWeekday
	dtpgDayOfYear
	dtpgDayOfWeekInMonth
	dtpgDay
	dtpgDayPeriod
	dtpgHour
	dtpgMinute
	dtpgSecond
	dtpgFractionalSecond
	dtpgZone
	dtpgFieldCount
)

const (
	dtNarrow  = -0x101
	dtShorter = -0x102
	dtShort   = -0x103
	dtLong    = -0x104
	dtNumeric = 0x100
	dtDelta   = 0x10

	dtExtraField   = 0x10000
	dtMissingField = 0x1000

	dtpgMatchHourFieldLength = 1 << dtpgHour
	dtpgFixFractionalSeconds = 1
	dtpgFractionalMask       = 1 << dtpgFractionalSecond
	dtpgSecondAndFractional  = 1<<dtpgSecond | 1<<dtpgFractionalSecond
)

type dtType struct {
	ch     byte
	field  int
	typ    int
	minLen int
}

var dtTypes = []dtType{
	{'G', dtpgEra, dtShort, 1}, {'G', dtpgEra, dtLong, 4}, {'G', dtpgEra, dtNarrow, 5},
	{'y', dtpgYear, dtNumeric, 1}, {'Y', dtpgYear, dtNumeric + dtDelta, 1}, {'u', dtpgYear, dtNumeric + 2*dtDelta, 1},
	{'r', dtpgYear, dtNumeric + 3*dtDelta, 1}, {'U', dtpgYear, dtShort, 1}, {'U', dtpgYear, dtLong, 4}, {'U', dtpgYear, dtNarrow, 5},
	{'Q', dtpgQuarter, dtNumeric, 1}, {'Q', dtpgQuarter, dtShort, 3}, {'Q', dtpgQuarter, dtLong, 4}, {'Q', dtpgQuarter, dtNarrow, 5},
	{'q', dtpgQuarter, dtNumeric + dtDelta, 1}, {'q', dtpgQuarter, dtShort - dtDelta, 3}, {'q', dtpgQuarter, dtLong - dtDelta, 4}, {'q', dtpgQuarter, dtNarrow - dtDelta, 5},
	{'M', dtpgMonth, dtNumeric, 1}, {'M', dtpgMonth, dtShort, 3}, {'M', dtpgMonth, dtLong, 4}, {'M', dtpgMonth, dtNarrow, 5},
	{'L', dtpgMonth, dtNumeric + dtDelta, 1}, {'L', dtpgMonth, dtShort - dtDelta, 3}, {'L', dtpgMonth, dtLong - dtDelta, 4}, {'L', dtpgMonth, dtNarrow - dtDelta, 5},
	{'l', dtpgMonth, dtNumeric + dtDelta, 1},
	{'w', dtpgWeekOfYear, dtNumeric, 1},
	{'W', dtpgWeekOfMonth, dtNumeric, 1},
	{'E', dtpgWeekday, dtShort, 1}, {'E', dtpgWeekday, dtLong, 4}, {'E', dtpgWeekday, dtNarrow, 5}, {'E', dtpgWeekday, dtShorter, 6},
	{'c', dtpgWeekday, dtNumeric + 2*dtDelta, 1}, {'c', dtpgWeekday, dtShort - 2*dtDelta, 3}, {'c', dtpgWeekday, dtLong - 2*dtDelta, 4},
	{'c', dtpgWeekday, dtNarrow - 2*dtDelta, 5}, {'c', dtpgWeekday, dtShorter - 2*dtDelta, 6},
	{'e', dtpgWeekday, dtNumeric + dtDelta, 1}, {'e', dtpgWeekday, dtShort - dtDelta, 3}, {'e', dtpgWeekday, dtLong - dtDelta, 4},
	{'e', dtpgWeekday, dtNarrow - dtDelta, 5}, {'e', dtpgWeekday, dtShorter - dtDelta, 6},
	{'d', dtpgDay, dtNumeric, 1}, {'g', dtpgDay, dtNumeric + dtDelta, 1},
	{'D', dtpgDayOfYear, dtNumeric, 1},
	{'F', dtpgDayOfWeekInMonth, dtNumeric, 1},
	{'a', dtpgDayPeriod, dtShort, 1}, {'a', dtpgDayPeriod, dtLong, 4}, {'a', dtpgDayPeriod, dtNarrow, 5},
	{'b', dtpgDayPeriod, dtShort - dtDelta, 1}, {'b', dtpgDayPeriod, dtLong - dtDelta, 4}, {'b', dtpgDayPeriod, dtNarrow - dtDelta, 5},
	{'B', dtpgDayPeriod, dtShort - 3*dtDelta, 1}, {'B', dtpgDayPeriod, dtLong - 3*dtDelta, 4}, {'B', dtpgDayPeriod, dtNarrow - 3*dtDelta, 5},
	{'H', dtpgHour, dtNumeric + 10*dtDelta, 1}, {'k', dtpgHour, dtNumeric + 11*dtDelta, 1},
	{'h', dtpgHour, dtNumeric, 1}, {'K', dtpgHour, dtNumeric + dtDelta, 1},
	{'J', dtpgHour, dtNumeric + 5*dtDelta, 1}, {'j', dtpgHour, dtNumeric + 6*dtDelta, 1}, {'C', dtpgHour, dtNumeric + 7*dtDelta, 1},
	{'m', dtpgMinute, dtNumeric, 1},
	{'s', dtpgSecond, dtNumeric, 1}, {'A', dtpgSecond, dtNumeric + dtDelta, 1},
	{'S', dtpgFractionalSecond, dtNumeric, 1},
	{'v', dtpgZone, dtShort - 2*dtDelta, 1}, {'v', dtpgZone, dtLong - 2*dtDelta, 4},
	{'z', dtpgZone, dtShort, 1}, {'z', dtpgZone, dtLong, 4},
	{'Z', dtpgZone, dtNarrow - dtDelta, 1}, {'Z', dtpgZone, dtLong - dtDelta, 4}, {'Z', dtpgZone, dtShort - dtDelta, 5},
	{'O', dtpgZone, dtShort - dtDelta, 1}, {'O', dtpgZone, dtLong - dtDelta, 4},
	{'V', dtpgZone, dtShort - dtDelta, 1}, {'V', dtpgZone, dtLong - dtDelta, 2}, {'V', dtpgZone, dtLong - 1 - dtDelta, 3}, {'V', dtpgZone, dtLong - 2 - dtDelta, 4},
	{'X', dtpgZone, dtNarrow - dtDelta, 1}, {'X', dtpgZone, dtShort - dtDelta, 2}, {'X', dtpgZone, dtLong - dtDelta, 4},
	{'x', dtpgZone, dtNarrow - dtDelta, 1}, {'x', dtpgZone, dtShort - dtDelta, 2}, {'x', dtpgZone, dtLong - dtDelta, 4},
}

// dtCanonicalIndex implements FormatParser::getCanonicalIndex (strict).
func dtCanonicalIndex(s string) int {
	if s == "" {
		return -1
	}
	ch := s[0]
	for i := 1; i < len(s); i++ {
		if s[i] != ch {
			return -1
		}
	}
	for i := 0; i < len(dtTypes); i++ {
		if dtTypes[i].ch != ch {
			continue
		}
		if i+1 >= len(dtTypes) || dtTypes[i+1].ch != ch {
			return i
		}
		if dtTypes[i+1].minLen <= len(s) {
			continue
		}
		return i
	}
	return -1
}

// dtTokens implements FormatParser::set: runs of one ASCII letter, or single
// other characters.
func dtTokens(pattern string) []string {
	var items []string
	for i := 0; i < len(pattern); {
		c := pattern[i]
		if c|0x20 >= 'a' && c|0x20 <= 'z' {
			j := i + 1
			for j < len(pattern) && pattern[j] == c {
				j++
			}
			items = append(items, pattern[i:j])
			i = j
			continue
		}
		// one (possibly multi-byte) character
		j := i + 1
		for j < len(pattern) && pattern[j]&0xC0 == 0x80 {
			j++
		}
		items = append(items, pattern[i:j])
		i = j
	}
	return items
}

// dtQuoteLiteral implements FormatParser::getQuoteLiteral from items[i] (a
// quote); it returns the literal text (with quotes) and the index of its
// closing quote.
func dtQuoteLiteral(items []string, i int) (string, int) {
	var b strings.Builder
	if items[i] == "'" {
		b.WriteString(items[i])
		i++
	}
	for i < len(items) {
		if items[i] == "'" {
			if i+1 < len(items) && items[i+1] == "'" {
				b.WriteString("''")
				i += 2
				continue
			}
			b.WriteString(items[i])
			break
		}
		b.WriteString(items[i])
		i++
	}
	return b.String(), i
}

func dtIsPatternSeparator(items []string, field string) bool {
	for i := 0; i < len(field); i++ {
		c := field[i]
		if c == '\'' || c == '\\' || c == ' ' || c == ':' || c == '"' || c == ',' || c == '-' || (i < len(items) && items[i] != "" && items[i][0] == '.') {
			continue
		}
		return false
	}
	return true
}

type dtFields struct {
	chars   [dtpgFieldCount]byte
	lengths [dtpgFieldCount]int8
}

func (f *dtFields) set(field int, ch byte, n int) { f.chars[field], f.lengths[field] = ch, int8(n) }
func (f *dtFields) clear(field int)               { f.chars[field], f.lengths[field] = 0, 0 }
func (f *dtFields) empty(field int) bool          { return f.lengths[field] == 0 }

func (f *dtFields) String() string {
	var b strings.Builder
	for i := range dtpgFieldCount {
		for range f.lengths[i] {
			b.WriteByte(f.chars[i])
		}
	}
	return b.String()
}

func (f *dtFields) firstChar() byte {
	for i := range dtpgFieldCount {
		if f.lengths[i] != 0 {
			return f.chars[i]
		}
	}
	return 0
}

type dtSkeleton struct {
	typ                   [dtpgFieldCount]int
	original, base        dtFields
	addedDefaultDayPeriod bool
}

// dtMatch implements DateTimeMatcher::set.
func dtMatch(pattern string) dtSkeleton {
	var sk dtSkeleton
	items := dtTokens(pattern)
	for i := 0; i < len(items); i++ {
		v := items[i]
		if v[0] == '\'' {
			_, i = dtQuoteLiteral(items, i)
			continue
		}
		ci := dtCanonicalIndex(v)
		if ci < 0 {
			continue
		}
		row := dtTypes[ci]
		sk.original.set(row.field, v[0], len(v))
		sk.base.set(row.field, row.ch, row.minLen)
		sub := row.typ
		if row.typ > 0 {
			sub += len(v)
		}
		sk.typ[row.field] = sub
	}
	if !sk.original.empty(dtpgMinute) && !sk.original.empty(dtpgFractionalSecond) && sk.original.empty(dtpgSecond) {
		for _, row := range dtTypes {
			if row.field == dtpgSecond {
				sk.original.set(dtpgSecond, row.ch, row.minLen)
				sk.base.set(dtpgSecond, row.ch, row.minLen)
				sk.typ[dtpgSecond] = row.typ
				if row.typ > 0 {
					sk.typ[dtpgSecond] = row.typ + 1
				}
				break
			}
		}
	}
	if !sk.original.empty(dtpgHour) {
		if c := sk.original.chars[dtpgHour]; c == 'h' || c == 'K' {
			if sk.original.empty(dtpgDayPeriod) {
				for _, row := range dtTypes {
					if row.field == dtpgDayPeriod {
						sk.original.set(dtpgDayPeriod, row.ch, row.minLen)
						sk.base.set(dtpgDayPeriod, row.ch, row.minLen)
						sk.typ[dtpgDayPeriod] = row.typ
						sk.addedDefaultDayPeriod = true
						break
					}
				}
			}
		} else {
			sk.original.clear(dtpgDayPeriod)
			sk.base.clear(dtpgDayPeriod)
			sk.typ[dtpgDayPeriod] = 0
		}
	}
	return sk
}

func (s *dtSkeleton) fieldMask() int {
	m := 0
	for i := range dtpgFieldCount {
		if s.typ[i] != 0 {
			m |= 1 << i
		}
	}
	return m
}

// skeletonString implements PtnSkeleton::getSkeleton.
func (s *dtSkeleton) skeletonString() string {
	out := s.original.String()
	if s.addedDefaultDayPeriod {
		if i := strings.IndexByte(out, 'a'); i >= 0 {
			out = out[:i] + out[i+1:]
		}
	}
	return out
}

// distance implements DateTimeMatcher::getDistance.
func (s *dtSkeleton) distance(other *dtSkeleton, includeMask int) (int, int, int) {
	result, missing, extra := 0, 0, 0
	for i := range dtpgFieldCount {
		my := 0
		if includeMask&(1<<i) != 0 {
			my = s.typ[i]
		}
		ot := other.typ[i]
		if my == ot {
			continue
		}
		switch {
		case my == 0:
			result += dtExtraField
			extra |= 1 << i
		case ot == 0:
			result += dtMissingField
			missing |= 1 << i
		default:
			d := my - ot
			if d < 0 {
				d = -d
			}
			result += d
		}
	}
	return result, missing, extra
}

type dtElem struct {
	base      string
	skeleton  dtSkeleton
	pattern   string
	specified bool
}

type dtpg struct {
	boot              [52][]*dtElem
	hourChar          byte
	dateTimeFormats   [4]string // full, long, medium, short
	appendItemFormats [dtpgFieldCount]string
	appendItemNames   [dtpgFieldCount]string
	availableKeys     map[string]bool
}

func bootIndex(c byte) int {
	switch {
	case c >= 'A' && c <= 'Z':
		return int(c - 'A')
	case c >= 'a' && c <= 'z':
		return 26 + int(c-'a')
	}
	return -1
}

func (g *dtpg) patternFromBase(base string) (*dtElem, bool) {
	if base == "" {
		return nil, false
	}
	bi := bootIndex(base[0])
	if bi < 0 {
		return nil, false
	}
	for _, e := range g.boot[bi] {
		if e.base == base {
			return e, true
		}
	}
	return nil, false
}

// patternFromSkeleton implements getPatternFromSkeleton comparing originals.
func (g *dtpg) patternFromSkeleton(sk *dtSkeleton) (*dtElem, *dtSkeleton) {
	bi := bootIndex(sk.base.firstChar())
	if bi < 0 {
		return nil, nil
	}
	for _, e := range g.boot[bi] {
		if e.skeleton.original == sk.original {
			if e.specified {
				return e, &e.skeleton
			}
			return e, nil
		}
	}
	return nil, nil
}

func (g *dtpg) add(base string, sk dtSkeleton, pattern string, specified bool) {
	bi := bootIndex(base[0])
	if bi < 0 {
		return
	}
	for _, e := range g.boot[bi] {
		if e.base == base && e.skeleton.typ == sk.typ {
			e.pattern = pattern
			e.specified = specified
			return
		}
	}
	g.boot[bi] = append(g.boot[bi], &dtElem{base: base, skeleton: sk, pattern: pattern, specified: specified})
}

// addPattern implements addPatternWithOptionalSkeleton.
func (g *dtpg) addPattern(pattern, skeleton string, override bool) {
	src := pattern
	if skeleton != "" {
		src = skeleton
	}
	sk := dtMatch(src)
	base := sk.base.String()
	if base == "" {
		return
	}
	if e, ok := g.patternFromBase(base); ok && (!e.specified || (skeleton != "" && !override)) {
		if !override {
			return
		}
	}
	if e, specified := g.patternFromSkeleton(&sk); e != nil {
		if !override || (skeleton != "" && specified != nil) {
			return
		}
	}
	g.add(base, sk, pattern, skeleton != "")
}

// CLDR "en" Gregorian data (ICU 78 locales/en.txt; root adds nothing new).
var enAvailableFormats = [][2]string{
	{"Bh", "h B"}, {"Bhm", "h:mm B"}, {"Bhms", "h:mm:ss B"}, {"E", "ccc"}, {"EBh", "E h B"}, {"EBhm", "E h:mm B"},
	{"EBhms", "E h:mm:ss B"}, {"EHm", "E HH:mm"}, {"EHms", "E HH:mm:ss"}, {"Ed", "d E"}, {"Eh", "E h a"}, {"Ehm", "E h:mm a"},
	{"Ehms", "E h:mm:ss a"}, {"Gy", "y G"}, {"GyM", "M/y G"}, {"GyMEd", "E, M/d/y G"}, {"GyMMM", "MMM y G"},
	{"GyMMMEd", "E, MMM d, y G"}, {"GyMMMd", "MMM d, y G"}, {"GyMd", "M/d/y G"}, {"H", "HH"}, {"Hm", "HH:mm"},
	{"Hms", "HH:mm:ss"}, {"Hmsv", "HH:mm:ss v"}, {"Hmv", "HH:mm v"}, {"Hv", "HH v"}, {"M", "L"}, {"MEd", "E, M/d"},
	{"MMM", "LLL"}, {"MMMEd", "E, MMM d"}, {"MMMMd", "MMMM d"}, {"MMMd", "MMM d"}, {"Md", "M/d"}, {"d", "d"},
	{"h", "h a"}, {"hm", "h:mm a"}, {"hms", "h:mm:ss a"}, {"hmsv", "h:mm:ss a v"}, {"hmv", "h:mm a v"}, {"hv", "h a v"},
	{"ms", "mm:ss"}, {"y", "y"}, {"yM", "M/y"}, {"yMEd", "E, M/d/y"}, {"yMMM", "MMM y"}, {"yMMMEd", "E, MMM d, y"},
	{"yMMMM", "MMMM y"}, {"yMMMd", "MMM d, y"}, {"yMd", "M/d/y"}, {"yQQQ", "QQQ y"}, {"yQQQQ", "QQQQ y"},
}

// enDateTimePatterns: time full..short, then date full..short.
var enDateTimePatterns = [8]string{
	"h:mm:ss a zzzz", "h:mm:ss a z", "h:mm:ss a", "h:mm a",
	"EEEE, MMMM d, y", "MMMM d, y", "MMM d, y", "M/d/yy",
}

// enDateAtTime is DateTimePatterns%atTime (full, long, medium, short).
var enDateAtTime = [4]string{"{1} 'at' {0}", "{1} 'at' {0}", "{1}, {0}", "{1}, {0}"}

var (
	dtpgMu    sync.Mutex
	dtpgCache = map[[2]byte]*dtpg{}
)

// enGenerator returns the "en" generator for a default hour character
// ('h' for en, or the -u-hc- keyword's).
func enGenerator(hourChar byte) *dtpg { return enGeneratorFor(hourChar, false) }

// enGeneratorFor optionally skips the standard date/time patterns
// (createInstanceNoStdPat, used by SimpleDateFormat for time styles).
func enGeneratorFor(hourChar byte, noStd bool) *dtpg {
	key := [2]byte{hourChar, 0}
	if noStd {
		key[1] = 1
	}
	dtpgMu.Lock()
	defer dtpgMu.Unlock()
	if g := dtpgCache[key]; g != nil {
		return g
	}
	g := &dtpg{hourChar: hourChar, dateTimeFormats: enDateAtTime, availableKeys: map[string]bool{}}
	for _, c := range "GyQMwWEDFdaHmsSv" {
		g.addPattern(string(c), "", false)
	}
	if !noStd {
		for _, p := range enDateTimePatterns {
			g.addPattern(p, "", false)
		}
	}
	for f, format := range map[int]string{
		dtpgDay: "{0} ({2}: {1})", dtpgWeekday: "{0} {1}", dtpgEra: "{0} {1}", dtpgHour: "{0} ({2}: {1})",
		dtpgMinute: "{0} ({2}: {1})", dtpgMonth: "{0} ({2}: {1})", dtpgQuarter: "{0} ({2}: {1})", dtpgSecond: "{0} ({2}: {1})",
		dtpgZone: "{0} {1}", dtpgWeekOfYear: "{0} ({2}: {1})", dtpgYear: "{0} {1}",
	} {
		g.appendItemFormats[f] = format
	}
	for f := range g.appendItemFormats {
		if g.appendItemFormats[f] == "" {
			g.appendItemFormats[f] = "{0} ├{2}: {1}┤"
		}
	}
	g.appendItemNames = [dtpgFieldCount]string{"era", "year", "quarter", "month", "week", "week of month", "day of the week",
		"day of year", "weekday of the month", "day", "AM/PM", "hour", "minute", "second", "F14", "time zone"}
	for _, kv := range enAvailableFormats {
		if !g.availableKeys[kv[0]] {
			g.availableKeys[kv[0]] = true
			g.addPattern(kv[1], kv[0], true)
		}
	}
	dtpgCache[key] = g
	return g
}

// dtRequest is the per-call matcher state (ICU's dtMatcher + distanceInfo).
type dtRequest struct {
	g         *dtpg
	matchHour bool // UDATPG_MATCH_HOUR_FIELD_LENGTH
	sk        dtSkeleton
	missing   int
	extra     int
}

// bestRaw implements getBestRaw.
func (q *dtRequest) bestRaw(includeMask int) (*dtElem, *dtSkeleton) {
	best := 0x7fffffff
	bestMissing := -1
	var bestElem *dtElem
	var bestSpecified *dtSkeleton
	for bi := range q.g.boot {
		for _, trial := range q.g.boot[bi] {
			d, missing, extra := q.sk.distance(&trial.skeleton, includeMask)
			if d < best || (d == best && bestMissing < missing) {
				best, bestMissing = d, missing
				bestElem, bestSpecified = q.g.patternFromSkeleton(&trial.skeleton)
				q.missing, q.extra = missing, extra
				if d == 0 {
					return bestElem, bestSpecified
				}
			}
		}
	}
	return bestElem, bestSpecified
}

// adjustFieldTypes implements adjustFieldTypes with
// UDATPG_MATCH_HOUR_FIELD_LENGTH (the only option V8 passes).
func (q *dtRequest) adjustFieldTypes(pattern string, specified *dtSkeleton, fixFractional bool) string {
	var b strings.Builder
	items := dtTokens(pattern)
	for i := 0; i < len(items); i++ {
		field := items[i]
		if field[0] == '\'' {
			lit, j := dtQuoteLiteral(items, i)
			b.WriteString(lit)
			i = j
			continue
		}
		if dtIsPatternSeparator(items, field) {
			b.WriteString(field)
			continue
		}
		ci := dtCanonicalIndex(field)
		if ci < 0 {
			b.WriteString(field)
			continue
		}
		row := dtTypes[ci]
		tv := row.field
		switch {
		case fixFractional && tv == dtpgSecond:
			field += "." + strings.Repeat(string(q.sk.original.chars[dtpgFractionalSecond]), int(q.sk.original.lengths[dtpgFractionalSecond]))
		case q.sk.typ[tv] != 0:
			reqChar := q.sk.original.chars[tv]
			reqLen := int(q.sk.original.lengths[tv])
			if reqChar == 'E' && reqLen < 3 {
				reqLen = 3
			}
			adjLen := reqLen
			if tv == dtpgMinute || tv == dtpgSecond || tv == dtpgHour && !q.matchHour {
				adjLen = len(field)
			} else if specified != nil && reqChar != 'c' && reqChar != 'e' {
				skelLen := int(specified.original.lengths[tv])
				patNumeric := row.typ > 0
				reqNumeric := q.sk.typ[tv] > 0
				if skelLen == reqLen || patNumeric != reqNumeric {
					adjLen = len(field)
				}
			}
			c := field[0]
			if tv != dtpgHour && tv != dtpgMonth && tv != dtpgWeekday && (tv != dtpgYear || reqChar == 'Y') {
				c = reqChar
			}
			if c == 'E' && adjLen < 3 {
				c = 'e'
			}
			if tv == dtpgHour && q.g.hourChar != 0 {
				switch {
				case reqChar == q.g.hourChar:
					c = q.g.hourChar
				case reqChar == 'h' && q.g.hourChar == 'K':
					c = 'K'
				case reqChar == 'H' && q.g.hourChar == 'k':
					c = 'k'
				case reqChar == 'k' && q.g.hourChar == 'H':
					c = 'H'
				case reqChar == 'K' && q.g.hourChar == 'h':
					c = 'h'
				}
			}
			field = strings.Repeat(string(c), adjLen)
		}
		b.WriteString(field)
	}
	return b.String()
}

// simpleFormat substitutes {0}, {1}, {2} like ICU's SimpleFormatter, where
// an apostrophe only quotes when followed by a brace or another apostrophe.
func simpleFormat(pattern string, args ...string) string {
	var b strings.Builder
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '\'' && i+1 < len(pattern) {
			switch pattern[i+1] {
			case '\'':
				b.WriteByte('\'')
				i++
				continue
			case '{', '}':
				j := strings.IndexByte(pattern[i+1:], '\'')
				if j < 0 {
					b.WriteString(pattern[i+1:])
					return b.String()
				}
				b.WriteString(pattern[i+1 : i+1+j])
				i += j + 1
				continue
			}
		}
		if c == '{' && i+2 < len(pattern) && pattern[i+2] == '}' && pattern[i+1] >= '0' && pattern[i+1] <= '9' {
			if n := int(pattern[i+1] - '0'); n < len(args) {
				b.WriteString(args[n])
			}
			i += 2
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// bestAppending implements getBestAppending.
func (q *dtRequest) bestAppending(missingFields int) string {
	if missingFields == 0 {
		return ""
	}
	elem, specified := q.bestRaw(missingFields)
	if elem == nil {
		return ""
	}
	result := q.adjustFieldTypes(elem.pattern, specified, false)
	if q.missing == 0 {
		return result
	}
	last := 0
	for q.missing != 0 {
		if last == q.missing {
			break
		}
		if q.missing&dtpgSecondAndFractional == dtpgFractionalMask && missingFields&dtpgSecondAndFractional == dtpgSecondAndFractional {
			result = q.adjustFieldTypes(result, specified, true)
			q.missing &^= dtpgFractionalMask
			continue
		}
		starting := q.missing
		elem, specified = q.bestRaw(q.missing)
		temp := q.adjustFieldTypes(elem.pattern, specified, false)
		found := starting &^ q.missing
		top := topBit(found)
		if f := q.g.appendItemFormats[top]; f != "" {
			result = simpleFormat(f, result, temp, "'"+q.g.appendItemNames[top]+"'")
		}
		last = q.missing
	}
	return result
}

func topBit(mask int) int {
	if mask == 0 {
		return 0
	}
	i := 0
	for mask != 0 {
		mask >>= 1
		i++
	}
	if i-1 > dtpgZone {
		return dtpgZone
	}
	return i - 1
}

// bestPattern implements getBestPattern(skeleton, UDATPG_MATCH_HOUR_FIELD_LENGTH).
func (g *dtpg) bestPattern(skeleton string) string { return g.bestPatternOpts(skeleton, true) }

// bestPatternOpts implements getBestPattern, mapping the 'j' metacharacter.
func (g *dtpg) bestPatternOpts(skeleton string, matchHour bool) string {
	if strings.IndexByte(skeleton, 'j') >= 0 {
		var mapped strings.Builder
		for i := 0; i < len(skeleton); i++ {
			if skeleton[i] != 'j' {
				mapped.WriteByte(skeleton[i])
				continue
			}
			extra := 0
			for i+1 < len(skeleton) && skeleton[i+1] == 'j' {
				extra++
				i++
			}
			dayPeriodLen := 1
			if extra >= 2 {
				dayPeriodLen = 3 + extra>>1
			}
			if g.hourChar == 'H' || g.hourChar == 'k' {
				dayPeriodLen = 0
			}
			mapped.WriteString(strings.Repeat("a", dayPeriodLen))
			mapped.WriteString(strings.Repeat(string(g.hourChar), 1+extra&1))
		}
		skeleton = mapped.String()
	}
	q := &dtRequest{g: g, sk: dtMatch(skeleton), matchHour: matchHour}
	elem, specified := q.bestRaw(-1)
	if elem != nil && q.missing == 0 && q.extra == 0 {
		return q.adjustFieldTypes(elem.pattern, specified, false)
	}
	needed := q.sk.fieldMask()
	dateMask := 1<<dtpgDayPeriod - 1
	timeMask := 1<<dtpgFieldCount - 1 - dateMask
	datePattern := q.bestAppending(needed & dateMask)
	timePattern := q.bestAppending(needed & timeMask)
	if datePattern == "" {
		return timePattern
	}
	if timePattern == "" {
		return datePattern
	}
	style := 3
	switch q.sk.base.lengths[dtpgMonth] {
	case 4:
		style = 1
		if q.sk.base.lengths[dtpgWeekday] > 0 {
			style = 0
		}
	case 3:
		style = 2
	}
	return simpleFormat(g.dateTimeFormats[style], timePattern, datePattern)
}
