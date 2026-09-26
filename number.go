package toolscript

import (
	"math"
	"math/big"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/dop251/goja/parser"
)

// numberToString implements Number::toString(x) for radix 10.
func numberToString(f float64) string {
	switch {
	case math.IsNaN(f):
		return "NaN"
	case math.IsInf(f, 1):
		return "Infinity"
	case math.IsInf(f, -1):
		return "-Infinity"
	case f == 0:
		return "0"
	}
	if f == math.Trunc(f) && math.Abs(f) < 1e15 {
		return strconv.FormatInt(int64(f), 10)
	}
	sign := ""
	if f < 0 {
		sign, f = "-", -f
	}
	// Shortest round-trip digits, then ECMAScript's layout rules.
	e := strconv.FormatFloat(f, 'e', -1, 64)
	mant, expPart, _ := strings.Cut(e, "e")
	digits := strings.Replace(mant, ".", "", 1)
	exp, _ := strconv.Atoi(expPart)
	k, n := len(digits), exp+1
	switch {
	case k <= n && n <= 21:
		return sign + digits + strings.Repeat("0", n-k)
	case 0 < n && n <= 21:
		return sign + digits[:n] + "." + digits[n:]
	case -6 < n && n <= 0:
		return sign + "0." + strings.Repeat("0", -n) + digits
	}
	es := "+"
	if n-1 < 0 {
		es = "-"
	}
	ev := strconv.Itoa(abs(n - 1))
	if k == 1 {
		return sign + digits + "e" + es + ev
	}
	return sign + digits[:1] + "." + digits[1:] + "e" + es + ev
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// numberToRadix implements Number.prototype.toString(radix) for integers;
// fractional values in other radixes are rare and use the host fallback.
func numberToRadix(f float64, radix int) (string, bool) {
	if radix == 10 {
		return numberToString(f), true
	}
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return numberToString(f), true
	}
	if f != math.Trunc(f) || math.Abs(f) > 1<<53 {
		return "", false
	}
	return strconv.FormatInt(int64(f), radix), true
}

// toFixed implements Number.prototype.toFixed exactly (ties round up).
func toFixed(x float64, digits int) string {
	if math.IsNaN(x) {
		return "NaN"
	}
	if math.Abs(x) >= 1e21 || math.IsInf(x, 0) {
		return numberToString(x)
	}
	sign := ""
	if x < 0 {
		sign, x = "-", -x
	}
	r := new(big.Rat).SetFloat64(x)
	r.Mul(r, new(big.Rat).SetInt(new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(digits)), nil)))
	r.Add(r, big.NewRat(1, 2))
	n := new(big.Int).Quo(r.Num(), r.Denom())
	s := n.String()
	if digits > 0 {
		if len(s) <= digits {
			s = strings.Repeat("0", digits-len(s)+1) + s
		}
		s = s[:len(s)-digits] + "." + s[len(s)-digits:]
	}
	return sign + s
}

func isJSSpace(r rune) bool { return strings.ContainsRune(parser.WhitespaceChars, r) }

func trimJS(s string) string { return strings.Trim(s, parser.WhitespaceChars) }

// stringToNumber implements StringToNumber.
func stringToNumber(s string) float64 {
	s = trimJS(s)
	if s == "" {
		return 0
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
			return parseRadixDigits(s[2:], base, true)
		}
	}
	switch s {
	case "Infinity", "+Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	if n := decimalPrefix(s); n != len(s) {
		return math.NaN()
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil && !isRangeErr(err) {
		return math.NaN()
	}
	return f
}

func isRangeErr(err error) bool {
	ne, ok := err.(*strconv.NumError)
	return ok && ne.Err == strconv.ErrRange
}

// parseRadixDigits parses digits of base; strict requires all characters valid.
func parseRadixDigits(s string, base int, strict bool) float64 {
	if s == "" {
		return math.NaN()
	}
	var v float64
	n := 0
	for _, c := range s {
		d := digitVal(c)
		if d < 0 || d >= base {
			if strict {
				return math.NaN()
			}
			break
		}
		v = v*float64(base) + float64(d)
		n++
	}
	if n == 0 {
		return math.NaN()
	}
	if base == 10 || v > 1<<53 {
		// Re-parse for correct rounding of long decimal/binary strings.
		if f, err := strconv.ParseInt(s[:n], base, 64); err == nil {
			return float64(f)
		}
		if base == 10 {
			f, _ := strconv.ParseFloat(s[:n], 64)
			return f
		}
	}
	return v
}

func digitVal(c rune) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'z':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'Z':
		return int(c-'A') + 10
	}
	return -1
}

// decimalPrefix returns the length of the longest StrDecimalLiteral prefix
// (sign, digits, fraction, exponent), or of "Infinity" forms.
func decimalPrefix(s string) int {
	i := 0
	if i < len(s) && (s[i] == '+' || s[i] == '-') {
		i++
	}
	if strings.HasPrefix(s[i:], "Infinity") {
		return i + len("Infinity")
	}
	start := i
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	intDigits := i - start
	fracDigits := 0
	if i < len(s) && s[i] == '.' {
		j := i + 1
		for j < len(s) && s[j] >= '0' && s[j] <= '9' {
			j++
		}
		fracDigits = j - i - 1
		if intDigits > 0 || fracDigits > 0 {
			i = j
		}
	}
	if intDigits == 0 && fracDigits == 0 {
		return 0
	}
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		j := i + 1
		if j < len(s) && (s[j] == '+' || s[j] == '-') {
			j++
		}
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k > j {
			i = k
		}
	}
	return i
}

func parseFloatJS(s string) float64 {
	s = strings.TrimLeft(s, parser.WhitespaceChars)
	n := decimalPrefix(s)
	if n == 0 {
		return math.NaN()
	}
	p := s[:n]
	switch strings.TrimLeft(p, "+-") {
	case "Infinity":
		if p[0] == '-' {
			return math.Inf(-1)
		}
		return math.Inf(1)
	}
	f, err := strconv.ParseFloat(p, 64)
	if err != nil && !isRangeErr(err) {
		return math.NaN()
	}
	return f
}

func parseIntJS(s string, radix int) float64 {
	s = strings.TrimLeft(s, parser.WhitespaceChars)
	sign := 1.0
	if s != "" && (s[0] == '+' || s[0] == '-') {
		if s[0] == '-' {
			sign = -1
		}
		s = s[1:]
	}
	strip := true
	if radix != 0 {
		if radix < 2 || radix > 36 {
			return math.NaN()
		}
		if radix != 16 {
			strip = false
		}
	} else {
		radix = 10
	}
	if strip && len(s) >= 2 && s[0] == '0' && (s[1] == 'x' || s[1] == 'X') {
		s, radix = s[2:], 16
	}
	f := parseRadixDigits(s, radix, false)
	return sign * f
}

// ---- UTF-16 string helpers ----

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func utf16Len(s string) int {
	if isASCII(s) {
		return len(s)
	}
	n := 0
	for _, r := range s {
		n += utf16.RuneLen(r)
	}
	return n
}

func toUnits(s string) []uint16 { return utf16.Encode([]rune(s)) }

func fromUnits(u []uint16) string { return string(utf16.Decode(u)) }

func utf16At(s string, i int) (string, bool) {
	if isASCII(s) {
		if i < len(s) {
			return s[i : i+1], true
		}
		return "", false
	}
	u := toUnits(s)
	if i < len(u) {
		return fromUnits(u[i : i+1]), true
	}
	return "", false
}

func utf16Slice(s string, from, to int) string {
	if from >= to {
		return ""
	}
	if isASCII(s) {
		return s[from:to]
	}
	return fromUnits(toUnits(s)[from:to])
}

func utf16Index(s, sub string, from int) int {
	if isASCII(s) && isASCII(sub) {
		if from > len(s) {
			from = len(s)
		}
		i := strings.Index(s[from:], sub)
		if i < 0 {
			return -1
		}
		return i + from
	}
	u, v := toUnits(s), toUnits(sub)
	for i := from; i+len(v) <= len(u); i++ {
		if unitsEqual(u[i:i+len(v)], v) {
			return i
		}
	}
	return -1
}

func utf16LastIndex(s, sub string, from int) int {
	u, v := toUnits(s), toUnits(sub)
	start := min(from, len(u)-len(v))
	for i := start; i >= 0; i-- {
		if unitsEqual(u[i:i+len(v)], v) {
			return i
		}
	}
	return -1
}

func unitsEqual(a, b []uint16) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func compareUTF16(a, b string) int {
	if isASCII(a) && isASCII(b) {
		return strings.Compare(a, b)
	}
	u, v := toUnits(a), toUnits(b)
	for i := 0; i < len(u) && i < len(v); i++ {
		if u[i] != v[i] {
			if u[i] < v[i] {
				return -1
			}
			return 1
		}
	}
	return len(u) - len(v)
}
