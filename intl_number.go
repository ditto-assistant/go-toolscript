package toolscript

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// decQuantity is the decimal number ICU formats (icu::number::impl::
// DecimalQuantity): an exact decimal plus the display requirements set by
// rounding and integer width. Doubles enter as their shortest round-trip
// digits, which is what ICU rounds (see DecimalQuantity::roundToMagnitude:
// uncertain cases are re-done on the shortest representation).
type decQuantity struct {
	neg, nan, inf bool
	d             []byte // digit values 0..9, no leading or trailing zeros; empty is zero
	exp           int    // value = 0.d[0]d[1]... × 10^exp
	minInt        int    // ICU lReqPos
	minFrac       int    // ICU -rReqPos
	expo          int    // ICU exponent: compact/scientific power of ten (plural operands)

	// approx marks a double not yet rounded (ICU isApproximate): the
	// increment rounder divides ICU's fast approximation of orig, shifted by
	// the magnitude adjustments made since.
	approx bool
	orig   float64
	shift  int
	str    bool // came from a decimal string (an ICU Formattable decimal)
}

func decFromFloat(f float64) decQuantity {
	var q decQuantity
	if math.IsNaN(f) {
		q.nan = true
		return q
	}
	if math.Signbit(f) {
		q.neg = true
		f = -f
	}
	if math.IsInf(f, 0) {
		q.inf = true
		return q
	}
	if f == 0 {
		return q
	}
	var buf [32]byte
	b := strconv.AppendFloat(buf[:0], f, 'e', -1, 64)
	e := 0
	q.d = make([]byte, 0, 17)
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 'e' {
			e, _ = strconv.Atoi(string(b[i+1:]))
			break
		}
		if c != '.' {
			q.d = append(q.d, c-'0')
		}
	}
	q.exp = e + 1
	q.normalize()
	q.approx, q.orig = true, f
	return q
}

var doubleMultipliers = [...]float64{1e0, 1e1, 1e2, 1e3, 1e4, 1e5, 1e6, 1e7, 1e8, 1e9, 1e10, 1e11, 1e12, 1e13, 1e14, 1e15, 1e16, 1e17, 1e18, 1e19, 1e20, 1e21}

// fastDigits is ICU's DecimalQuantity::_setToDoubleFast: the digits ICU
// holds for a double before it is rounded. ok is false when ICU uses the
// exact (shortest) digits instead.
func fastDigits(n float64) (digits string, scale int, ok bool) {
	bits := math.Float64bits(n)
	exponent := int((bits&0x7ff0000000000000)>>52) - 0x3ff
	if exponent <= 52 && float64(int64(n)) == n {
		return "", 0, false
	}
	if exponent == -1023 || exponent == 1024 {
		return "", 0, false
	}
	fracLength := int(float64(52-exponent) / 3.32192809488736234787031942948939017586)
	if fracLength >= 0 {
		i := fracLength
		for ; i >= 22; i -= 22 {
			n = float64(n * 1e22)
		}
		n = float64(n * doubleMultipliers[i])
	} else {
		i := fracLength
		for ; i <= -22; i += 22 {
			n = float64(n / 1e22)
		}
		n = float64(n / doubleMultipliers[-i])
	}
	result := int64(math.Floor(float64(n + 0.5)))
	return strconv.FormatInt(result, 10), -fracLength, true
}

// decFromDigits builds a quantity from ASCII digits with the decimal point
// after intLen digits.
func decFromDigits(neg bool, digits string, intLen int) decQuantity {
	q := decQuantity{neg: neg, d: make([]byte, len(digits)), exp: intLen}
	for i := 0; i < len(digits); i++ {
		q.d[i] = digits[i] - '0'
	}
	q.normalize()
	return q
}

func (q *decQuantity) normalize() {
	i := 0
	for i < len(q.d) && q.d[i] == 0 {
		i++
	}
	if i > 0 {
		q.d = q.d[i:]
		q.exp -= i
	}
	j := len(q.d)
	for j > 0 && q.d[j-1] == 0 {
		j--
	}
	q.d = q.d[:j]
	if len(q.d) == 0 {
		q.exp = 0
	}
}

// zeroish is ICU's isZeroish: true for zero, NaN and infinity (no digits).
func (q *decQuantity) zeroish() bool { return len(q.d) == 0 }

func (q *decQuantity) isZero() bool { return len(q.d) == 0 && !q.nan && !q.inf }

// magnitude of the leading digit (callers check zeroish first).
func (q *decQuantity) magnitude() int { return q.exp - 1 }

// mag0 is the magnitude, 0 for zero (ICU's getRoundingMagnitudeSignificant).
func (q *decQuantity) mag0() int {
	if q.zeroish() {
		return 0
	}
	return q.exp - 1
}

func (q *decQuantity) adjustMagnitude(delta int) {
	if !q.zeroish() {
		q.exp += delta
		q.shift += delta
	}
}

// hasFraction reports nonzero digits below the decimal point (plural operand t).
func (q *decQuantity) hasFraction() bool {
	return len(q.d) > 0 && q.exp-len(q.d)-q.expo < 0
}

// digitAt returns the digit at a power of ten.
func (q *decQuantity) digitAt(mag int) byte {
	i := q.exp - 1 - mag
	if i < 0 || i >= len(q.d) {
		return 0
	}
	return q.d[i]
}

func (q *decQuantity) clone() decQuantity {
	c := *q
	c.d = append([]byte(nil), q.d...)
	return c
}

type roundingMode uint8

const (
	rmCeil roundingMode = iota
	rmFloor
	rmExpand
	rmTrunc
	rmHalfCeil
	rmHalfFloor
	rmHalfExpand
	rmHalfTrunc
	rmHalfEven
)

var roundingModeNames = []string{"ceil", "floor", "expand", "trunc", "halfCeil", "halfFloor", "halfExpand", "halfTrunc", "halfEven"}

// roundUp decides the direction for a value strictly between two candidates.
// section: -1 below the midpoint, 0 at it, 1 above it.
func roundUp(mode roundingMode, section int, neg, odd bool) bool {
	switch mode {
	case rmExpand:
		return true
	case rmTrunc:
		return false
	case rmCeil:
		return !neg
	case rmFloor:
		return neg
	}
	if section != 0 {
		return section > 0
	}
	switch mode {
	case rmHalfExpand:
		return true
	case rmHalfTrunc:
		return false
	case rmHalfCeil:
		return !neg
	case rmHalfFloor:
		return neg
	}
	return odd // halfEven
}

// roundToMagnitude rounds to a multiple of 10^m.
func (q *decQuantity) roundToMagnitude(m int, mode roundingMode) {
	if len(q.d) == 0 {
		return
	}
	k := q.exp - m // digits kept
	if k >= len(q.d) {
		return
	}
	section := -1
	odd := false
	if k >= 0 {
		switch f := q.d[k]; {
		case f > 5:
			section = 1
		case f < 5:
			section = -1
		case len(q.d) > k+1:
			section = 1
		default:
			section = 0
		}
		if k > 0 {
			odd = q.d[k-1]%2 == 1
		}
	}
	up := roundUp(mode, section, q.neg, odd)
	if k <= 0 {
		if up {
			q.d = append(q.d[:0], 1)
			q.exp = m + 1
		} else {
			q.d = q.d[:0]
			q.exp = 0
		}
		return
	}
	q.d = q.d[:k]
	if up {
		i := k - 1
		for i >= 0 && q.d[i] == 9 {
			q.d[i] = 0
			i--
		}
		if i < 0 {
			q.d = append(q.d[:0], 1)
			q.exp++
		} else {
			q.d[i]++
		}
	}
	q.normalize()
}

// roundToSignificant rounds half-even to p significant digits (decNumber
// arithmetic in a context of p digits).
func (q *decQuantity) roundToSignificant(p int) {
	if len(q.d) > p {
		q.roundToMagnitude(q.exp-p, rmHalfEven)
	}
}

// mulSmall multiplies by a small positive integer.
func (q *decQuantity) mulSmall(n int) {
	if len(q.d) == 0 || n == 1 {
		return
	}
	out := make([]byte, len(q.d)+3)
	carry := 0
	j := len(out) - 1
	for i := len(q.d) - 1; i >= 0; i-- {
		v := int(q.d[i])*n + carry
		out[j] = byte(v % 10)
		carry = v / 10
		j--
	}
	for carry > 0 {
		out[j] = byte(carry % 10)
		carry /= 10
		j--
	}
	grow := len(out) - 1 - j - len(q.d)
	q.d = out[j+1:]
	q.exp += grow
	q.normalize()
}

// roundToIncrement rounds to a multiple of inc × 10^mag like ICU: 1 and 5
// round exactly; other increments divide and multiply back in decNumber
// arithmetic with max(34, digits) digits of precision.
func (q *decQuantity) roundToIncrement(inc, mag int, mode roundingMode) {
	if len(q.d) == 0 {
		return
	}
	switch inc {
	case 1:
		q.roundToMagnitude(mag, mode)
		return
	}
	if q.approx && inc != 5 {
		// decNumber division sees ICU's fast approximation of the double.
		if digits, scale, ok := fastDigits(q.orig); ok {
			f := decFromDigits(q.neg, digits, len(digits)+scale)
			q.d, q.exp = f.d, f.exp+q.shift
			if len(q.d) == 0 {
				return
			}
		}
	}
	c, t := inc, 0
	for c%10 == 0 {
		c /= 10
		t++
	}
	// q / (c × 10^(mag+t)) = q × (10^j / c) × 10^-(j+mag+t)
	j, mult := 0, 1
	switch c {
	case 2:
		j, mult = 1, 5
	case 5:
		j, mult = 1, 2
	case 25:
		j, mult = 2, 4
	}
	exact := inc == 5
	prec := len(q.d)
	if prec < 34 {
		prec = 34
	}
	q.mulSmall(mult)
	q.exp -= j + mag + t
	if !exact {
		q.roundToSignificant(prec)
	}
	q.roundToMagnitude(0, mode)
	if len(q.d) == 0 {
		return
	}
	prec = len(q.d)
	if prec < 34 {
		prec = 34
	}
	q.mulSmall(c)
	q.exp += mag + t
	if !exact {
		q.roundToSignificant(prec)
	}
}

// pluralI is ICU's plural operand i: the integer digits, truncated to the
// lowest 18 (toLong(true)).
func (q *decQuantity) pluralI() uint64 {
	if len(q.d) == 0 {
		return 0
	}
	upper := q.exp + q.expo - 1
	if upper > 17 {
		upper = 17
	}
	var v uint64
	for m := upper; m >= 0; m-- {
		v = v*10 + uint64(q.digitAt(m-q.expo))
	}
	return v
}

// pluralV is ICU's plural operand v: visible fraction digits.
func (q *decQuantity) pluralV() int {
	lower := -q.minFrac
	if len(q.d) > 0 {
		if s := q.exp - len(q.d); s < lower {
			lower = s
		}
	} else if lower > 0 {
		lower = 0
	}
	v := -lower - q.expo
	if v < 0 {
		return 0
	}
	return v
}

// float is ICU's toDouble (plural operand n), exponent included.
func (q *decQuantity) float() float64 {
	if q.nan {
		return math.NaN()
	}
	if q.inf {
		return math.Inf(1)
	}
	if len(q.d) == 0 {
		return 0
	}
	var b strings.Builder
	b.WriteString("0.")
	for _, c := range q.d {
		b.WriteByte('0' + c)
	}
	b.WriteString("e")
	b.WriteString(strconv.Itoa(q.exp + q.expo))
	f, _ := strconv.ParseFloat(b.String(), 64)
	return f
}

// intlMathValue is ToIntlMathematicalValue: a Number or an exact decimal
// string. isString marks values that came from strings (formatted exactly).
func (r *rt) intlMathValue(v any) (decQuantity, error) {
	if isObjectValue(v) {
		p, err := r.toPrimitiveHint(v, "number")
		if err != nil {
			return decQuantity{}, err
		}
		v = p
	}
	switch t := v.(type) {
	case float64:
		return decFromFloat(t), nil
	case string:
		return decFromJSString(t), nil
	}
	f, err := r.toNumber(v)
	if err != nil {
		return decQuantity{}, err
	}
	return decFromFloat(f), nil
}

// decFromJSString parses a StringNumericLiteral exactly (V8 hands decimal
// strings to ICU as decimals; non-decimal integers are exact BigInts).
func decFromJSString(s string) decQuantity {
	s = trimJS(s)
	if s == "" {
		return decQuantity{}
	}
	if len(s) > 2 && s[0] == '0' {
		base := 0
		switch s[1] {
		case 'x', 'X':
			base = 16
		case 'o', 'O':
			base = 8
		case 'b', 'B':
			base = 2
		}
		if base != 0 {
			n, ok := new(big.Int).SetString(s[2:], base)
			if !ok || strings.ContainsAny(s[2:], "_+-") {
				return decQuantity{nan: true}
			}
			if n.Cmp(big.NewInt(1<<53-1)) < 0 {
				f, _ := new(big.Float).SetInt(n).Float64()
				return decFromFloat(f)
			}
			q := decFromDigits(false, n.String(), len(n.String()))
			q.str = true
			return q
		}
	}
	neg := false
	body := s
	if body[0] == '+' || body[0] == '-' {
		neg = body[0] == '-'
		body = body[1:]
	}
	if body == "Infinity" {
		return decQuantity{neg: neg, inf: true}
	}
	// StrUnsignedDecimalLiteral
	i := 0
	intStart := i
	for i < len(body) && body[i] >= '0' && body[i] <= '9' {
		i++
	}
	intPart := body[intStart:i]
	fracPart := ""
	if i < len(body) && body[i] == '.' {
		i++
		fs := i
		for i < len(body) && body[i] >= '0' && body[i] <= '9' {
			i++
		}
		fracPart = body[fs:i]
	}
	if intPart == "" && fracPart == "" {
		return decQuantity{nan: true}
	}
	exp := 0
	if i < len(body) && (body[i] == 'e' || body[i] == 'E') {
		i++
		eneg := false
		if i < len(body) && (body[i] == '+' || body[i] == '-') {
			eneg = body[i] == '-'
			i++
		}
		es := i
		for i < len(body) && body[i] >= '0' && body[i] <= '9' {
			if exp < 1e9 {
				exp = exp*10 + int(body[i]-'0')
			}
			i++
		}
		if i == es {
			return decQuantity{nan: true}
		}
		if eneg {
			exp = -exp
		}
	}
	if i != len(body) {
		return decQuantity{nan: true}
	}
	// V8 decides Infinity by StringToDouble.
	if f, err := strconv.ParseFloat(s, 64); err != nil && math.IsInf(f, 0) {
		return decQuantity{neg: neg, inf: true}
	}
	q := decFromDigits(neg, intPart+fracPart, len(intPart))
	q.adjustMagnitude(exp)
	q.shift = 0
	q.str = true
	return q
}

// ---------------------------------------------------------------------------
// Intl.NumberFormat
//
// The pipeline mirrors ICU's NumberFormatterImpl as V8 configures it:
// scale (percent) -> notation (scientific/compact choose a multiplier and
// round) or rounding -> integer width -> digits with grouping -> inner
// modifier (exponent) -> middle modifier (sign, currency, percent, compact
// affix) -> outer modifier (unit or currency long names).

type currencyData struct {
	digits           int
	symbol, narrow   string
	nameOne, nameOth string
}

func intlCurrency(code string) currencyData {
	if c, ok := intlCurrencyTable[code]; ok {
		return c
	}
	return currencyData{2, code, code, code, code}
}

const (
	nfDecimal = iota
	nfPercent
	nfCurrency
	nfUnit
)

const (
	cdSymbol = iota
	cdNarrow
	cdCode
	cdName
)

// unit widths, in the order of the generated data
const (
	uwLong = iota
	uwShort
	uwNarrow
)

const (
	ntStandard = iota
	ntScientific
	ntEngineering
	ntCompact
)

const (
	ugAuto = iota
	ugAlways
	ugMin2
	ugOff
)

const (
	sdAuto = iota
	sdNever
	sdAlways
	sdExceptZero
	sdNegative
)

const (
	rtFraction = iota
	rtSignificant
	rtMore
	rtLess
)

var (
	nfStyleNames    = []string{"decimal", "percent", "currency", "unit"}
	nfNotationNames = []string{"standard", "scientific", "engineering", "compact"}
	nfSignNames     = []string{"auto", "never", "always", "exceptZero", "negative"}
	nfCurrencyNames = []string{"symbol", "narrowSymbol", "code", "name"}
	nfWidthNames    = []string{"long", "short", "narrow"}
)

// intlDigits is the result of SetNumberFormatDigitOptions.
type intlDigits struct {
	minInt           int
	minFrac, maxFrac int
	minSig, maxSig   int
	roundingType     int
	increment        int
	mode             roundingMode
	strip            bool // trailingZeroDisplay: "stripIfInteger"
}

type numberFormat struct {
	locale          string
	style           int
	currency        string
	cur             currencyData
	currencyDisplay int
	accounting      bool // currencySign "accounting" (as read)
	unit            string
	unitNum         string
	unitPer         string
	unitWidth       int
	notation        int
	compactLong     bool
	grouping        int
	sign            int
	dig             intlDigits
	ranged          bool // the skeleton-built range formatter
}

// intlEnumOption is GetOption with a list of allowed strings; it returns the
// index of the value, or def when undefined.
func (r *rt) intlEnumOption(o *intlOptions, prop string, allowed []string, def int) (int, error) {
	s, ok, err := r.intlStringOption(o, prop)
	if err != nil || !ok {
		return def, err
	}
	for i, a := range allowed {
		if a == s {
			return i, nil
		}
	}
	return 0, r.rangeError("Value " + s + " out of range for " + o.service + " options property " + prop)
}

// intlStringOption is GetOption(..., "string", empty, undefined).
func (r *rt) intlStringOption(o *intlOptions, prop string) (string, bool, error) {
	v, err := r.optionValue(o, prop)
	if err != nil || isUndefined(v) {
		return "", false, err
	}
	s, err := r.toString(v)
	return s, err == nil, err
}

// intlNumberingSystemOption reads and validates the numberingSystem option.
func (r *rt) intlNumberingSystemOption(o *intlOptions) (string, bool, error) {
	s, ok, err := r.intlStringOption(o, "numberingSystem")
	if err != nil || !ok {
		return "", false, err
	}
	if !isWellFormedUnicodeType(s) {
		return "", false, r.rangeError("Invalid numberingSystem : " + s)
	}
	return s, true, nil
}

func validNumberingSystem(s string) bool {
	for _, n := range intlNumberingSystems {
		if n == s {
			return true
		}
	}
	return false
}

// intlNumberingLocale resolves the locale of a service with the relevant
// extension key "nu". Only Latin digits are formatted; a script asking for
// another (supported) numbering system is declined rather than formatted
// with the wrong digits.
func (r *rt) intlNumberingLocale(tags []string, nu string, hasNu bool) (string, error) {
	res, err := r.resolveLocale(stringArray(tags), map[string][]string{"nu": {"latn"}})
	if err != nil {
		return "", err
	}
	extNu := ""
	for _, tag := range tags {
		t, _, _ := parseLanguageTag(tag)
		if _, ok := lookupLocale(t.base); ok {
			extNu = t.ext["nu"]
			break
		}
	}
	effective := "latn"
	if hasNu && validNumberingSystem(nu) {
		effective = nu
	} else if validNumberingSystem(extNu) {
		effective = extNu
	}
	if effective != "latn" {
		return "", errRuntimeUnsupported("Intl numbering system " + effective)
	}
	return res.locale, nil
}

func validRoundingIncrement(n int) bool {
	switch n {
	case 1, 2, 5, 10, 20, 25, 50, 100, 200, 250, 500, 1000, 2000, 2500, 5000:
		return true
	}
	return false
}

// intlDigitOptions implements SetNumberFormatDigitOptions as V8 does.
func (r *rt) intlDigitOptions(o *intlOptions, mnfdDefault, mxfdDefault int, compact bool) (intlDigits, error) {
	var d intlDigits
	var err error
	if d.minInt, err = r.numberOption(o, "minimumIntegerDigits", 1, 21, 1); err != nil {
		return d, err
	}
	var raw [4]any
	for i, p := range []string{"minimumFractionDigits", "maximumFractionDigits", "minimumSignificantDigits", "maximumSignificantDigits"} {
		if raw[i], err = r.optionValue(o, p); err != nil {
			return d, err
		}
	}
	if d.increment, err = r.numberOption(o, "roundingIncrement", 1, 5000, 1); err != nil {
		return d, err
	}
	if !validRoundingIncrement(d.increment) {
		return d, r.rangeError("roundingIncrement value is out of range.")
	}
	mode, err := r.intlEnumOption(o, "roundingMode", roundingModeNames, int(rmHalfExpand))
	if err != nil {
		return d, err
	}
	d.mode = roundingMode(mode)
	priority, err := r.intlEnumOption(o, "roundingPriority", []string{"auto", "morePrecision", "lessPrecision"}, 0)
	if err != nil {
		return d, err
	}
	tzd, err := r.intlEnumOption(o, "trailingZeroDisplay", []string{"auto", "stripIfInteger"}, 0)
	if err != nil {
		return d, err
	}
	d.strip = tzd == 1
	hasSd := !isUndefined(raw[2]) || !isUndefined(raw[3])
	hasFd := !isUndefined(raw[0]) || !isUndefined(raw[1])
	needSd, needFd := true, true
	if priority == 0 {
		needSd = hasSd
		if needSd || (!hasFd && compact) {
			needFd = false
		}
	}
	if needSd {
		if hasSd {
			d.minSig = 1
			if !isUndefined(raw[2]) {
				if d.minSig, err = r.defaultNumberOption(raw[2], "minimumSignificantDigits", 1, 21); err != nil {
					return d, err
				}
			}
			d.maxSig = 21
			if !isUndefined(raw[3]) {
				if d.maxSig, err = r.defaultNumberOption(raw[3], "maximumSignificantDigits", d.minSig, 21); err != nil {
					return d, err
				}
			}
		} else {
			d.minSig, d.maxSig = 1, 21
		}
	}
	if needFd {
		if hasFd {
			mnfd, mxfd := -1, -1
			if !isUndefined(raw[0]) {
				if mnfd, err = r.defaultNumberOption(raw[0], "minimumFractionDigits", 0, 100); err != nil {
					return d, err
				}
			}
			if !isUndefined(raw[1]) {
				if mxfd, err = r.defaultNumberOption(raw[1], "maximumFractionDigits", 0, 100); err != nil {
					return d, err
				}
			}
			switch {
			case isUndefined(raw[0]):
				mnfd = min(mnfdDefault, mxfd)
			case isUndefined(raw[1]):
				mxfd = max(mxfdDefault, mnfd)
			case mnfd > mxfd:
				return d, r.rangeError("maximumFractionDigits value is out of range.")
			}
			d.minFrac, d.maxFrac = mnfd, mxfd
		} else {
			d.minFrac, d.maxFrac = mnfdDefault, mxfdDefault
		}
	}
	switch {
	case !needSd && !needFd:
		d.minFrac, d.maxFrac, d.minSig, d.maxSig = 0, 0, 1, 2
		d.roundingType = rtMore
	case priority == 1:
		d.roundingType = rtMore
	case priority == 2:
		d.roundingType = rtLess
	case hasSd:
		d.roundingType = rtSignificant
	default:
		d.roundingType = rtFraction
	}
	if d.increment != 1 {
		if d.roundingType != rtFraction {
			return d, r.typeError("RoundingType is not fractionDigits")
		}
		if d.maxFrac != d.minFrac {
			return d, r.rangeError("maximumFractionDigits value is out of range.")
		}
	}
	return d, nil
}

// rounder is ICU's RoundingImpl for the precision V8 configures.
type rounder struct {
	pass bool
	d    *intlDigits
}

const intlNoDisplay = 1 << 30

func (rd rounder) apply(q *decQuantity) {
	if rd.pass || q.nan || q.inf {
		return
	}
	defer func() { q.approx = false }()
	d := rd.d
	resolved := 0
	switch {
	case d.increment != 1:
		q.roundToIncrement(d.increment, -d.maxFrac, d.mode)
		resolved = d.minFrac
	case d.roundingType == rtFraction:
		q.roundToMagnitude(-d.maxFrac, d.mode)
		resolved = d.minFrac
	case d.roundingType == rtSignificant:
		q.roundToMagnitude(q.mag0()-d.maxSig+1, d.mode)
		resolved = max(0, -(q.mag0() - d.minSig + 1))
		if q.zeroish() && d.minSig > 0 && q.minInt < 1 {
			q.minInt = 1
		}
	default:
		relaxed := d.roundingType == rtMore
		mag1 := -d.maxFrac
		mag2 := q.mag0() - d.maxSig + 1
		m := max(mag1, mag2)
		if relaxed {
			m = min(mag1, mag2)
		}
		if !q.zeroish() {
			upper := q.magnitude()
			q.roundToMagnitude(m, d.mode)
			if !q.zeroish() && q.magnitude() != upper && mag1 == mag2 {
				mag2++
			}
		}
		disp1 := intlNoDisplay
		if d.minFrac != 0 {
			disp1 = -d.minFrac
		}
		disp2 := q.mag0() - d.minSig + 1
		var disp int
		if relaxed == (mag2 <= mag1) {
			disp = disp2
		} else {
			disp = disp1
		}
		resolved = max(0, -disp)
	}
	if !d.strip || q.hasFraction() {
		q.minFrac = resolved
	}
}

// chooseMultiplierAndApply is RoundingImpl::chooseMultiplierAndApply.
func (rd rounder) chooseMultiplierAndApply(q *decQuantity, multiplier func(int) int) int {
	magnitude := q.magnitude()
	mult := multiplier(magnitude)
	q.adjustMagnitude(mult)
	rd.apply(q)
	if q.zeroish() || q.magnitude() == magnitude+mult {
		return mult
	}
	mult2 := multiplier(magnitude + 1)
	if mult == mult2 {
		return mult
	}
	q.adjustMagnitude(mult2 - mult)
	rd.apply(q)
	return mult2
}

func compactMultiplier(mag int) int {
	if mag < 0 {
		return 0
	}
	if mag > 14 {
		mag = 14
	}
	if mag < 3 {
		return 0
	}
	return -(mag / 3 * 3)
}

var (
	compactShort = []string{"K", "M", "B", "T"}
	compactLong  = []string{" thousand", " million", " billion", " trillion"}
)

// process runs the quantity through scale, notation and rounding. It
// returns the compact magnitude (-1 without a compact pattern).
func (nf *numberFormat) process(q *decQuantity) (compactMag int) {
	compactMag = -1
	if nf.style == nfPercent {
		q.adjustMagnitude(2)
	}
	rd := rounder{d: &nf.dig}
	switch nf.notation {
	case ntScientific, ntEngineering:
		if q.nan || q.inf {
			break
		}
		exponent := 0
		if q.zeroish() {
			rd.apply(q)
		} else {
			interval := 1
			if nf.notation == ntEngineering {
				interval = 3
			}
			exponent = -rd.chooseMultiplierAndApply(q, func(mag int) int {
				shown := 1
				if interval > 1 {
					shown = (mag%interval+interval)%interval + 1
				}
				return shown - mag - 1
			})
		}
		q.expo += exponent
	case ntCompact:
		mag, mult := 0, 0
		if q.zeroish() {
			rd.apply(q)
		} else {
			mult = rd.chooseMultiplierAndApply(q, compactMultiplier)
			mag = q.mag0() - mult
		}
		if mag >= 3 {
			compactMag = min(mag, 14)
		}
		q.expo -= mult
	default:
		rd.apply(q)
	}
	if q.minInt < nf.dig.minInt {
		q.minInt = nf.dig.minInt
	}
	return compactMag
}

// minGrouping returns the minimum grouping digits (0: no grouping).
func (nf *numberFormat) minGrouping() int {
	switch nf.grouping {
	case ugOff:
		return 0
	case ugMin2:
		return 2
	case ugAuto:
		if nf.notation == ntCompact {
			return 2 // V8 leaves ICU's compact default (min2) in place
		}
	}
	return 1
}

// numberParts renders the digits (ICU writeNumber).
func numberParts(q *decQuantity, minGrouping int, parts []intlPart) []intlPart {
	if q.nan {
		return append(parts, intlPart{typ: "nan", value: "NaN"})
	}
	if q.inf {
		return append(parts, intlPart{typ: "infinity", value: "∞"})
	}
	intCount := q.minInt
	if len(q.d) > 0 && q.exp > intCount {
		intCount = q.exp
	}
	lower := -q.minFrac
	if len(q.d) > 0 {
		lower = min(lower, q.exp-len(q.d))
	} else {
		lower = min(lower, 0)
	}
	if intCount == 0 && lower >= 0 {
		return append(parts, intlPart{typ: "integer", value: "0"})
	}
	group := minGrouping > 0 && intCount-3 >= minGrouping
	var b []byte
	for m := intCount - 1; m >= 0; m-- {
		b = append(b, '0'+q.digitAt(m))
		if group && m >= 3 && (m-3)%3 == 0 {
			parts = append(parts, intlPart{typ: "integer", value: string(b)}, intlPart{typ: "group", value: ","})
			b = b[:0]
		}
	}
	if len(b) > 0 {
		parts = append(parts, intlPart{typ: "integer", value: string(b)})
	}
	if lower < 0 {
		b = b[:0]
		for m := -1; m >= lower; m-- {
			b = append(b, '0'+q.digitAt(m))
		}
		parts = append(parts, intlPart{typ: "decimal", value: "."}, intlPart{typ: "fraction", value: string(b)})
	}
	return parts
}

// exponentParts renders the scientific inner modifier.
func exponentParts(exp int, parts []intlPart) []intlPart {
	parts = append(parts, intlPart{typ: "exponentSeparator", value: "E"})
	if exp < 0 {
		parts = append(parts, intlPart{typ: "exponentMinusSign", value: "-"})
		exp = -exp
	}
	return append(parts, intlPart{typ: "exponentInteger", value: strconv.Itoa(exp)})
}

// pattern sign types (PatternStringUtils::resolveSignDisplay)
const (
	signPos = iota
	signNeg
	signPlus
)

func (nf *numberFormat) signType(q *decQuantity) int {
	zero := q.zeroish() && !q.inf
	neg := q.neg && !q.nan
	switch nf.sign {
	case sdAuto:
		if neg {
			return signNeg
		}
		return signPos
	case sdAlways:
		if neg {
			return signNeg
		}
		return signPlus
	case sdExceptZero:
		switch {
		case zero:
			return signPos
		case neg:
			return signNeg
		}
		return signPlus
	case sdNegative:
		if neg && !zero {
			return signNeg
		}
	}
	return signPos
}

func (nf *numberFormat) currencyText() string {
	switch nf.currencyDisplay {
	case cdNarrow:
		return nf.cur.narrow
	case cdCode:
		return nf.currency
	}
	return nf.cur.symbol
}

// percentUnit reports style "unit" with a percent unit that ICU formats with
// the percent pattern instead of CLDR unit data. The range formatter,
// rebuilt from the skeleton, sees "percent-per-x" as one compound unit.
func (nf *numberFormat) percentUnit() bool {
	return nf.style == nfUnit && nf.unitNum == "percent" && nf.unitWidth != uwLong && nf.notation != ntCompact && !(nf.ranged && nf.unitPer != "")
}

func (nf *numberFormat) forRange() *numberFormat {
	c := *nf
	c.ranged = true
	return &c
}

// middle returns the pattern affixes (the middle modifier).
func (nf *numberFormat) middle(q *decQuantity, compactMag, sign int, approx bool) (pre, suf []intlPart) {
	currencyShort := nf.style == nfCurrency && nf.currencyDisplay != cdName
	negPattern := false
	switch {
	case compactMag >= 0:
		i := compactMag/3 - 1
		if currencyShort {
			pre = append(pre, intlPart{typ: "currency", value: nf.currencyText()})
			suf = append(suf, intlPart{typ: "compact", value: compactShort[i]})
		} else if nf.compactLong {
			suf = append(suf, intlPart{typ: "compact", value: compactLong[i]})
		} else {
			suf = append(suf, intlPart{typ: "compact", value: compactShort[i]})
		}
	case nf.style == nfPercent && nf.notation != ntCompact:
		suf = append(suf, intlPart{typ: "percentSign", value: "%"})
	case nf.percentUnit():
		suf = append(suf, intlPart{typ: "unit", value: "%"})
	case currencyShort:
		if sign == signNeg && nf.accounting && nf.sign != sdNever {
			negPattern = true
			pre = append(pre, intlPart{typ: "literal", value: "("}, intlPart{typ: "currency", value: nf.currencyText()})
			suf = append(suf, intlPart{typ: "literal", value: ")"})
		} else {
			pre = append(pre, intlPart{typ: "currency", value: nf.currencyText()})
		}
	}
	if negPattern {
		return pre, suf
	}
	var signParts []intlPart
	if approx {
		signParts = append(signParts, intlPart{typ: "approximatelySign", value: "~"})
	}
	switch sign {
	case signNeg:
		signParts = append(signParts, intlPart{typ: "minusSign", value: "-"})
	case signPlus:
		signParts = append(signParts, intlPart{typ: "plusSign", value: "+"})
	}
	if len(signParts) > 0 {
		pre = append(signParts, pre...)
	}
	return pre, suf
}

// hasOuter reports a long-name (outer) modifier.
func (nf *numberFormat) hasOuter() bool {
	switch nf.style {
	case nfUnit:
		return !nf.percentUnit()
	case nfCurrency:
		return nf.currencyDisplay == cdName
	case nfPercent:
		return nf.notation == ntCompact
	}
	return false
}

// outer returns the long-name affixes for a plural form (0 one, 1 other).
func (nf *numberFormat) outer(plural int) (pre, suf []intlPart) {
	var pattern, typ string
	switch nf.style {
	case nfCurrency:
		name := nf.cur.nameOth
		if plural == 0 {
			name = nf.cur.nameOne
		}
		return nil, []intlPart{{typ: "currency", value: " " + name}}
	case nfPercent:
		pattern, typ = intlUnitPatterns["percent"][uwShort][plural], "unit"
	default:
		pattern, typ = intlUnitPattern(nf.unitNum, nf.unitPer, nf.unitWidth, plural), "unit"
	}
	before, after, _ := strings.Cut(pattern, "{0}")
	if before != "" {
		pre = []intlPart{{typ: typ, value: before}}
	}
	if after != "" {
		suf = []intlPart{{typ: typ, value: after}}
	}
	return pre, suf
}

func intlUnitPattern(num, per string, width, plural int) string {
	if per == "" {
		return intlUnitPatterns[num][width][plural]
	}
	if p, ok := intlUnitCompound[num+"-per-"+per+"/"+strconv.Itoa(width)]; ok {
		return p[plural]
	}
	return intlUnitPatterns[num][width][plural] + intlUnitPerSuffix[per][width]
}

// pluralOf is the English cardinal rule over ICU's operands.
func pluralOf(q *decQuantity) int {
	if !q.nan && !q.inf && q.pluralI() == 1 && q.pluralV() == 0 {
		return 0
	}
	return 1
}

// nfFormatted is one formatted number with its modifiers kept apart (the
// range formatter collapses shared affixes).
type nfFormatted struct {
	q              decQuantity
	number         []intlPart // digits and exponent
	sciKey         string
	midPre, midSuf []intlPart
	plural         int
}

func (nf *numberFormat) formatStructured(x decQuantity, approx bool) nfFormatted {
	f := nfFormatted{q: x}
	q := &f.q
	compactMag := nf.process(q)
	f.number = numberParts(q, nf.minGrouping(), make([]intlPart, 0, 8))
	if (nf.notation == ntScientific || nf.notation == ntEngineering) && !q.nan && !q.inf {
		f.number = exponentParts(q.expo, f.number)
		f.sciKey = "E" + strconv.Itoa(q.expo)
	}
	f.midPre, f.midSuf = nf.middle(q, compactMag, nf.signType(q), approx)
	f.plural = pluralOf(q)
	return f
}

// currencySpacing reports whether a no-break space goes between a
// currency prefix and the number (ICU currency spacing for "en":
// currency [[:^S:]&[:^Z:]] next to a digit).
func currencySpacing(pre []intlPart, number []intlPart) bool {
	if len(pre) == 0 || len(number) == 0 || pre[len(pre)-1].typ != "currency" {
		return false
	}
	sym := pre[len(pre)-1].value
	last, _ := utf8.DecodeLastRuneInString(sym)
	if unicode.IsSymbol(last) || unicode.In(last, unicode.Z) {
		return false
	}
	c := number[0].value
	return c != "" && c[0] >= '0' && c[0] <= '9'
}

func withSource(parts []intlPart, source string) []intlPart {
	for i := range parts {
		parts[i].source = source
	}
	return parts
}

// wrap applies a modifier's affixes around content.
func wrap(pre, content, suf []intlPart, spacing bool, source string) []intlPart {
	out := make([]intlPart, 0, len(pre)+len(content)+len(suf)+1)
	out = append(out, withSource(append([]intlPart(nil), pre...), source)...)
	if spacing && currencySpacing(pre, content) {
		out = append(out, intlPart{typ: "literal", value: " ", source: source})
	}
	out = append(out, content...)
	return append(out, withSource(append([]intlPart(nil), suf...), source)...)
}

func (nf *numberFormat) formatParts(x decQuantity) []intlPart {
	f := nf.formatStructured(x, false)
	parts := wrap(f.midPre, f.number, f.midSuf, true, "")
	if nf.hasOuter() {
		pre, suf := nf.outer(f.plural)
		parts = wrap(pre, parts, suf, false, "")
	}
	return normalizeParts(parts)
}

// normalizeParts trims whitespace off fields into literals (ICU's
// FormattedValue trimming) and merges adjacent literals (V8 flattening).
func normalizeParts(parts []intlPart) []intlPart {
	out := parts[:0:0]
	add := func(p intlPart) {
		if p.value == "" {
			return
		}
		if n := len(out); n > 0 && p.typ == "literal" && out[n-1].typ == "literal" {
			out[n-1].value += p.value
			if out[n-1].source != p.source {
				out[n-1].source = "shared"
			}
			return
		}
		out = append(out, p)
	}
	for _, p := range parts {
		if p.typ == "literal" || p.typ == "group" {
			add(p)
			continue
		}
		body := strings.TrimLeftFunc(p.value, intlIgnorable)
		lead := p.value[:len(p.value)-len(body)]
		trimmed := strings.TrimRightFunc(body, intlIgnorable)
		trail := body[len(trimmed):]
		add(intlPart{typ: "literal", value: lead, source: p.source})
		add(intlPart{typ: p.typ, value: trimmed, source: p.source})
		add(intlPart{typ: "literal", value: trail, source: p.source})
	}
	return out
}

// intlIgnorable is ICU's DEFAULT_IGNORABLES: [[:Zs:][\u0009][:Bidi_Control:][:Variation_Selector:]].
func intlIgnorable(r rune) bool {
	switch {
	case r == '\t', unicode.Is(unicode.Zs, r), unicode.Is(unicode.Bidi_Control, r), unicode.Is(unicode.Variation_Selector, r):
		return true
	}
	return false
}

// rangeParts implements formatRange (ICU NumberRangeFormatterImpl with
// collapse "auto" and identity fallback "approximately"). Sources follow the
// spans ICU records, including its off-by-one when a collapsed currency
// prefix inserts currency spacing.
func (nf *numberFormat) rangeParts(x, y decQuantity) []intlPart {
	equalBefore := formattableEqual(&x, &y)
	// The range formatter gets exact doubles (Formattable::populateDecimalQuantity
	// rounds them to infinity), and V8 rebuilds it from the skeleton.
	x.approx, y.approx = false, false
	nf = nf.forRange()
	f1 := nf.formatStructured(x.clone(), false)
	f2 := nf.formatStructured(y.clone(), false)
	midEqual := partsEqual(f1.midPre, f2.midPre) && partsEqual(f1.midSuf, f2.midSuf)
	outer := nf.hasOuter()
	if f1.sciKey == f2.sciKey && midEqual && (equalBefore || decEqualDisplay(&f1.q, &f2.q)) {
		f := nf.formatStructured(x, true)
		parts := wrap(f.midPre, f.number, f.midSuf, true, "")
		if outer {
			pre, suf := nf.outer(f.plural)
			parts = wrap(pre, parts, suf, false, "")
		}
		return withSource(normalizeParts(parts), "shared")
	}
	collapseMiddle := midEqual && utf8.RuneCountInString(joinParts(f1.midPre)+joinParts(f1.midSuf)) > 1
	n1, n2 := f1.number, f2.number
	if !collapseMiddle {
		n1 = wrap(f1.midPre, n1, f1.midSuf, true, "")
		n2 = wrap(f2.midPre, n2, f2.midSuf, true, "")
	}
	infix := "–"
	if f1.sciKey != "" || (!collapseMiddle && len(f1.midPre)+len(f1.midSuf) > 0) {
		infix = " – "
	}
	index0, length1, length2 := 0, utf16Len(joinParts(n1)), utf16Len(joinParts(n2))
	parts := append(append(append([]intlPart(nil), n1...), intlPart{typ: "literal", value: infix}), n2...)
	if collapseMiddle {
		parts = wrap(f1.midPre, parts, f1.midSuf, true, "")
		index0 += utf16Len(joinParts(f1.midPre)) // excludes the currency spacing
	}
	if outer {
		// English plural ranges always resolve to "other".
		pre, suf := nf.outer(1)
		parts = wrap(pre, parts, suf, false, "")
		index0 += utf16Len(joinParts(pre))
	}
	index2 := index0 + length1 + utf16Len(infix)
	parts = normalizeParts(parts)
	pos := 0
	for i := range parts {
		end := pos + utf16Len(parts[i].value)
		switch {
		case pos >= index0 && end <= index0+length1:
			parts[i].source = "startRange"
		case pos >= index2 && end <= index2+length2:
			parts[i].source = "endRange"
		default:
			parts[i].source = "shared"
		}
		pos = end
	}
	return parts
}

func partsEqual(a, b []intlPart) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].typ != b[i].typ || a[i].value != b[i].value {
			return false
		}
	}
	return true
}

// formattableEqual is icu::Formattable::operator== on the values V8 passes
// to the range formatter: Numbers are doubles; decimal strings become int32,
// int64 or (lossy) double Formattables.
func formattableEqual(a, b *decQuantity) bool {
	ka, ia, fa := formattableOf(a)
	kb, ib, fb := formattableOf(b)
	if ka != kb {
		return false
	}
	if ka == 'd' {
		return fa == fb
	}
	return ia == ib
}

func formattableOf(q *decQuantity) (kind byte, i int64, f float64) {
	if !q.str {
		switch {
		case q.nan:
			return 'd', 0, math.NaN()
		case q.inf:
			return 'd', 0, math.Inf(boolSign(q.neg))
		case q.zeroish():
			return 'd', 0, 0
		}
		f = q.orig
		if q.neg {
			f = -f
		}
		return 'd', 0, f
	}
	if q.zeroish() {
		return 'l', 0, 0
	}
	if q.exp-len(q.d) >= 0 && q.exp <= 19 {
		n, ok := new(big.Int).SetString(digitString(q), 10)
		if ok {
			if q.neg {
				n.Neg(n)
			}
			if n.IsInt64() {
				v := n.Int64()
				if v >= math.MinInt32 && v <= math.MaxInt32 {
					return 'l', v, 0
				}
				return 'i', v, 0
			}
		}
	}
	f = q.float()
	if q.neg {
		f = -f
	}
	return 'd', 0, f
}

func boolSign(neg bool) int {
	if neg {
		return -1
	}
	return 1
}

// digitString renders an integral quantity's digits.
func digitString(q *decQuantity) string {
	b := make([]byte, 0, q.exp)
	for m := q.exp - 1; m >= 0; m-- {
		b = append(b, '0'+q.digitAt(m))
	}
	return string(b)
}

func decEqualValue(a, b *decQuantity) bool {
	if a.neg != b.neg || a.nan != b.nan || a.inf != b.inf || len(a.d) != len(b.d) {
		return false
	}
	if len(a.d) > 0 && a.exp != b.exp {
		return false
	}
	for i := range a.d {
		if a.d[i] != b.d[i] {
			return false
		}
	}
	return true
}

// decEqualDisplay is DecimalQuantity::operator== after rounding.
func decEqualDisplay(a, b *decQuantity) bool {
	return decEqualValue(a, b) && a.minInt == b.minInt && a.minFrac == b.minFrac
}

// --- construction ---

func init() {
	registerIntlKind(&intlKind{
		name: "NumberFormat",
		construct: func(r *rt, locales, options any) (any, error) {
			return r.newNumberFormat(locales, options, "Intl.NumberFormat")
		},
		boundGetter: "format",
		methods: map[string]intlMethod{
			"format": {1, func(r *rt, o *intlObject, args []any) (any, error) {
				x, err := r.intlMathValue(arg(args, 0))
				if err != nil {
					return nil, err
				}
				return joinParts(o.state.(*numberFormat).formatParts(x)), nil
			}},
			"formatToParts": {1, func(r *rt, o *intlObject, args []any) (any, error) {
				x, err := r.intlMathValue(arg(args, 0))
				if err != nil {
					return nil, err
				}
				return intlParts(o.state.(*numberFormat).formatParts(x)), nil
			}},
			"formatRange": {2, func(r *rt, o *intlObject, args []any) (any, error) {
				parts, err := r.numberRange(o.state.(*numberFormat), args)
				if err != nil {
					return nil, err
				}
				return joinParts(parts), nil
			}},
			"formatRangeToParts": {2, func(r *rt, o *intlObject, args []any) (any, error) {
				parts, err := r.numberRange(o.state.(*numberFormat), args)
				if err != nil {
					return nil, err
				}
				return intlParts(parts), nil
			}},
			"resolvedOptions": {0, func(r *rt, o *intlObject, _ []any) (any, error) {
				return o.state.(*numberFormat).resolvedOptions(), nil
			}},
		},
	})
	intlCurrencyCodes = func() []string { return intlCurrencyList }
	intlSanctionedUnits = func() []string { return intlUnitList }
}

// intlValueText renders a value the way V8 message templates print it.
func (r *rt) intlValueText(v any) (string, error) {
	switch t := v.(type) {
	case *array:
		return "[object Array]", nil
	case *dateValue:
		return "[object Date]", nil
	case *function:
		return "", errRuntimeUnsupported("Intl error text for a function")
	case *object, *hostObject, *collection, *iterator, *regexpValue, *intlObject:
		if o, ok := t.(*object); ok && o.errName != "" {
			return "", errRuntimeUnsupported("Intl error text for an Error")
		}
		return "#<Object>", nil
	}
	return r.toString(v)
}

func (r *rt) numberRange(nf *numberFormat, args []any) ([]intlPart, error) {
	start, end := arg(args, 0), arg(args, 1)
	if isUndefined(start) {
		return nil, r.typeError("Invalid start : undefined")
	}
	if isUndefined(end) {
		return nil, r.typeError("Invalid end : undefined")
	}
	x, err := r.intlMathValue(start)
	if err != nil {
		return nil, err
	}
	y, err := r.intlMathValue(end)
	if err != nil {
		return nil, err
	}
	for i, q := range []*decQuantity{&x, &y} {
		if q.nan {
			name, v := "start", start
			if i == 1 {
				name, v = "end", end
			}
			s, err := r.intlValueText(v)
			if err != nil {
				return nil, err
			}
			return nil, r.rangeError("Invalid " + name + " : " + s)
		}
	}
	return nf.rangeParts(x, y), nil
}

func wellFormedCurrency(s string) bool {
	if len(s) != 3 {
		return false
	}
	for i := 0; i < 3; i++ {
		if c := s[i] | 0x20; c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func sanctionedUnit(s string) bool {
	_, ok := intlUnitPatterns[s]
	return ok
}

func (r *rt) newNumberFormat(locales, options any, service string) (*numberFormat, error) {
	tags, err := r.canonicalizeLocaleList(locales)
	if err != nil {
		return nil, err
	}
	o, err := r.coerceOptions(options, service)
	if err != nil {
		return nil, err
	}
	if err := r.localeMatcherOption(o); err != nil {
		return nil, err
	}
	nu, hasNu, err := r.intlNumberingSystemOption(o)
	if err != nil {
		return nil, err
	}
	nf := &numberFormat{}
	if nf.locale, err = r.intlNumberingLocale(tags, nu, hasNu); err != nil {
		return nil, err
	}
	if nf.style, err = r.intlEnumOption(o, "style", nfStyleNames, nfDecimal); err != nil {
		return nil, err
	}
	cur, hasCur, err := r.intlStringOption(o, "currency")
	if err != nil {
		return nil, err
	}
	if hasCur {
		if !wellFormedCurrency(cur) {
			return nil, r.rangeError("Invalid currency code : " + cur)
		}
	} else if nf.style == nfCurrency {
		return nil, r.typeError("Currency code is required with currency style.")
	}
	cd, err := r.intlEnumOption(o, "currencyDisplay", []string{"code", "symbol", "name", "narrowSymbol"}, 1)
	if err != nil {
		return nil, err
	}
	nf.currencyDisplay = []int{cdCode, cdSymbol, cdName, cdNarrow}[cd]
	cs, err := r.intlEnumOption(o, "currencySign", []string{"standard", "accounting"}, 0)
	if err != nil {
		return nil, err
	}
	nf.accounting = cs == 1
	unit, hasUnit, err := r.intlStringOption(o, "unit")
	if err != nil {
		return nil, err
	}
	if hasUnit {
		num, per, ok := unit, "", sanctionedUnit(unit)
		if !ok {
			if i := strings.Index(unit, "-per-"); i >= 0 && !strings.Contains(unit[i+5:], "-per-") {
				num, per = unit[:i], unit[i+5:]
				ok = sanctionedUnit(num) && sanctionedUnit(per)
			}
		}
		if !ok {
			return nil, r.rangeError("Invalid unit argument for " + service + "() '" + unit + "'")
		}
		nf.unitNum, nf.unitPer = num, per
	} else if nf.style == nfUnit {
		return nil, r.typeError("Invalid unit argument for " + service + "() ''")
	}
	ud, err := r.intlEnumOption(o, "unitDisplay", []string{"short", "narrow", "long"}, 0)
	if err != nil {
		return nil, err
	}
	nf.unitWidth = []int{uwShort, uwNarrow, uwLong}[ud]
	switch nf.style {
	case nfCurrency:
		nf.currency = strings.ToUpper(cur)
		nf.cur = intlCurrency(nf.currency)
	case nfUnit:
		nf.unit = unit
	}
	if nf.notation, err = r.intlEnumOption(o, "notation", nfNotationNames, ntStandard); err != nil {
		return nil, err
	}
	mnfd, mxfd := 0, 3
	switch {
	case nf.style == nfCurrency && nf.notation == ntStandard:
		mnfd, mxfd = nf.cur.digits, nf.cur.digits
	case nf.style == nfPercent:
		mxfd = 0
	}
	if nf.dig, err = r.intlDigitOptions(o, mnfd, mxfd, nf.notation == ntCompact); err != nil {
		return nil, err
	}
	compact, err := r.intlEnumOption(o, "compactDisplay", []string{"short", "long"}, 0)
	if err != nil {
		return nil, err
	}
	nf.compactLong = compact == 1
	def := ugAuto
	if nf.notation == ntCompact {
		def = ugMin2
	}
	if nf.grouping, err = r.useGroupingOption(o, def); err != nil {
		return nil, err
	}
	if nf.sign, err = r.intlEnumOption(o, "signDisplay", nfSignNames, sdAuto); err != nil {
		return nil, err
	}
	return nf, nil
}

// useGroupingOption is GetBooleanOrStringNumberFormatOption.
func (r *rt) useGroupingOption(o *intlOptions, def int) (int, error) {
	v, err := r.optionValue(o, "useGrouping")
	if err != nil || isUndefined(v) {
		return def, err
	}
	if b, ok := v.(bool); ok && b {
		return ugAlways, nil
	}
	if !toBoolean(v) {
		return ugOff, nil
	}
	s, err := r.toString(v)
	if err != nil {
		return 0, err
	}
	switch s {
	case "true", "false":
		return def, nil
	case "min2":
		return ugMin2, nil
	case "auto":
		return ugAuto, nil
	case "always":
		return ugAlways, nil
	}
	return 0, r.rangeError("Value " + s + " out of range for " + o.service + " options property useGrouping")
}

// resolvedDigits appends the digit options as V8 reads them back from the
// ICU skeleton.
func (d *intlDigits) resolved(o *object, fraction, significant bool) {
	if fraction {
		o.set("minimumFractionDigits", float64(d.minFrac))
		o.set("maximumFractionDigits", float64(d.maxFrac))
	}
	if significant {
		o.set("minimumSignificantDigits", float64(d.minSig))
		o.set("maximumSignificantDigits", float64(d.maxSig))
	}
}

// roundingTail sets the rounding properties. hideStrip is V8 finding the
// stripIfInteger stem "/w" first inside "unit/w..." in the skeleton.
func (d *intlDigits) roundingTail(o *object, hideStrip bool) {
	o.set("roundingIncrement", float64(d.increment))
	o.set("roundingMode", roundingModeNames[d.mode])
	// V8 reads the priority from the skeleton stem "...#r"/"...@s", which a
	// following "/w" (stripIfInteger) hides.
	if d.strip {
		o.set("roundingPriority", "auto")
	} else {
		o.set("roundingPriority", []string{"auto", "auto", "morePrecision", "lessPrecision"}[d.roundingType])
	}
	if d.strip && !hideStrip {
		o.set("trailingZeroDisplay", "stripIfInteger")
	} else {
		o.set("trailingZeroDisplay", "auto")
	}
}

func (nf *numberFormat) resolvedOptions() *object {
	o := newObject(24)
	o.set("locale", nf.locale)
	o.set("numberingSystem", "latn")
	o.set("style", nfStyleNames[nf.style])
	switch nf.style {
	case nfCurrency:
		o.set("currency", nf.currency)
		o.set("currencyDisplay", nfCurrencyNames[nf.currencyDisplay])
		if nf.accounting && nf.sign != sdNever {
			o.set("currencySign", "accounting")
		} else {
			o.set("currencySign", "standard")
		}
	case nfUnit:
		o.set("unit", nf.unit)
		o.set("unitDisplay", nfWidthNames[nf.unitWidth])
	}
	d := &nf.dig
	o.set("minimumIntegerDigits", float64(d.minInt))
	d.resolved(o, d.roundingType != rtSignificant, d.roundingType != rtFraction)
	switch nf.grouping {
	case ugOff:
		o.set("useGrouping", false)
	case ugMin2:
		o.set("useGrouping", "min2")
	case ugAlways:
		o.set("useGrouping", "always")
	default:
		o.set("useGrouping", "auto")
	}
	o.set("notation", nfNotationNames[nf.notation])
	if nf.notation == ntCompact {
		if nf.compactLong {
			o.set("compactDisplay", "long")
		} else {
			o.set("compactDisplay", "short")
		}
	}
	o.set("signDisplay", nfSignNames[nf.sign])
	d.roundingTail(o, nf.style == nfUnit && strings.HasPrefix(nf.unit, "w"))
	return o
}

// numberToLocaleString implements Number.prototype.toLocaleString with
// locales or options.
func (r *rt) numberToLocaleString(f float64, locales, options any) (any, error) {
	nf, err := r.newNumberFormat(locales, options, "Number.prototype.toLocaleString")
	if err != nil {
		return nil, err
	}
	return joinParts(nf.formatParts(decFromFloat(f))), nil
}
