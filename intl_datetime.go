package toolscript

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Intl.DateTimeFormat, following V8's JSDateTimeFormat (option order, error
// texts, hour-cycle handling) over the ICU pattern generator in intl_dtpg.go.

func init() {
	registerIntlKind(&intlKind{
		name: "DateTimeFormat",
		construct: func(r *rt, locales, options any) (any, error) {
			return r.newDateTimeFormat(locales, options, dtfRequiredAny, dtfDefaultsDate, "Intl.DateTimeFormat")
		},
		boundGetter: "format",
		methods: map[string]intlMethod{
			"format": {1, func(r *rt, o *intlObject, args []any) (any, error) {
				return o.state.(*dateTimeFormat).formatValue(r, arg(args, 0), false)
			}},
			"formatToParts": {1, func(r *rt, o *intlObject, args []any) (any, error) {
				return o.state.(*dateTimeFormat).formatValue(r, arg(args, 0), true)
			}},
			"resolvedOptions": {0, func(r *rt, o *intlObject, _ []any) (any, error) {
				return o.state.(*dateTimeFormat).resolvedOptions(), nil
			}},
			"formatRange": {2, func(*rt, *intlObject, []any) (any, error) {
				return nil, errRuntimeUnsupported("Intl.DateTimeFormat.prototype.formatRange")
			}},
			"formatRangeToParts": {2, func(*rt, *intlObject, []any) (any, error) {
				return nil, errRuntimeUnsupported("Intl.DateTimeFormat.prototype.formatRangeToParts")
			}},
		},
	})
}

type dtfRequired int

const (
	dtfRequiredAny dtfRequired = iota
	dtfRequiredDate
	dtfRequiredTime
)

type dtfDefaults int

const (
	dtfDefaultsDate dtfDefaults = iota
	dtfDefaultsTime
	dtfDefaultsAll
)

type dateTimeFormat struct {
	locale    string
	timeZone  string // resolvedOptions().timeZone
	zone      dtfZone
	hourCycle string // "" when [[HourCycle]] is undefined
	dateStyle string
	timeStyle string
	pattern   string
	items     []dtfItem
	hasMinute bool
	hasSecond bool
}

// dtfZone computes UTC offsets and names for one time zone.
type dtfZone struct {
	id     string         // resolved identifier ("" for a fixed offset)
	loc    *time.Location // named zones
	offset int            // fixed-offset zones, seconds
}

var dtfStyles = []string{"full", "long", "medium", "short"}

// dtfComponent is one row of ECMA-402's Table 7 in V8's order.
type dtfComponent struct {
	prop    string
	values  []string
	pattern map[string]string // value -> skeleton characters
}

var dtfComponents = []dtfComponent{
	{"weekday", []string{"narrow", "long", "short"}, map[string]string{"narrow": "EEEEE", "long": "EEEE", "short": "EEE"}},
	{"era", []string{"narrow", "long", "short"}, map[string]string{"narrow": "GGGGG", "long": "GGGG", "short": "GGG"}},
	{"year", []string{"2-digit", "numeric"}, map[string]string{"2-digit": "yy", "numeric": "y"}},
	{"month", []string{"narrow", "long", "short", "2-digit", "numeric"}, map[string]string{"narrow": "MMMMM", "long": "MMMM", "short": "MMM", "2-digit": "MM", "numeric": "M"}},
	{"day", []string{"2-digit", "numeric"}, map[string]string{"2-digit": "dd", "numeric": "d"}},
	{"dayPeriod", []string{"narrow", "long", "short"}, map[string]string{"narrow": "BBBBB", "long": "BBBB", "short": "B"}},
	{"hour", []string{"2-digit", "numeric"}, nil},
	{"minute", []string{"2-digit", "numeric"}, map[string]string{"2-digit": "mm", "numeric": "m"}},
	{"second", []string{"2-digit", "numeric"}, map[string]string{"2-digit": "ss", "numeric": "s"}},
	{"timeZoneName", []string{"long", "short", "longOffset", "shortOffset", "longGeneric", "shortGeneric"},
		map[string]string{"long": "zzzz", "short": "z", "longOffset": "OOOO", "shortOffset": "O", "longGeneric": "vvvv", "shortGeneric": "v"}},
}

// dtfResolvedPairs is V8's GetPatternItems table used to read components
// back out of the final pattern (a plain substring search, as V8 does).
var dtfResolvedPairs = []struct {
	prop  string
	pairs [][2]string
}{
	{"weekday", [][2]string{{"EEEEE", "narrow"}, {"EEEE", "long"}, {"EEE", "short"}, {"ccccc", "narrow"}, {"cccc", "long"}, {"ccc", "short"}}},
	{"era", [][2]string{{"GGGGG", "narrow"}, {"GGGG", "long"}, {"GGG", "short"}}},
	{"year", [][2]string{{"yy", "2-digit"}, {"y", "numeric"}, {"YY", "2-digit"}, {"Y", "numeric"}}},
	{"month", [][2]string{{"MMMMM", "narrow"}, {"MMMM", "long"}, {"MMM", "short"}, {"MM", "2-digit"}, {"M", "numeric"},
		{"LLLLL", "narrow"}, {"LLLL", "long"}, {"LLL", "short"}, {"LL", "2-digit"}, {"L", "numeric"}}},
	{"day", [][2]string{{"dd", "2-digit"}, {"d", "numeric"}}},
	{"dayPeriod", [][2]string{{"BBBBB", "narrow"}, {"bbbbb", "narrow"}, {"BBBB", "long"}, {"bbbb", "long"}, {"B", "short"}, {"b", "short"}}},
	{"hour", [][2]string{{"HH", "2-digit"}, {"H", "numeric"}, {"hh", "2-digit"}, {"h", "numeric"}, {"kk", "2-digit"}, {"k", "numeric"}, {"KK", "2-digit"}, {"K", "numeric"}}},
	{"minute", [][2]string{{"mm", "2-digit"}, {"m", "numeric"}}},
	{"second", [][2]string{{"ss", "2-digit"}, {"s", "numeric"}}},
	{"timeZoneName", [][2]string{{"zzzz", "long"}, {"z", "short"}, {"OOOO", "longOffset"}, {"O", "shortOffset"}, {"vvvv", "longGeneric"}, {"v", "shortGeneric"}}},
}

func hourCycleChar(hc string) byte {
	switch hc {
	case "h11":
		return 'K'
	case "h12":
		return 'h'
	case "h23":
		return 'H'
	case "h24":
		return 'k'
	}
	return 0
}

// newDateTimeFormat implements CreateDateTimeFormat.
func (r *rt) newDateTimeFormat(locales, options any, required dtfRequired, defaults dtfDefaults, service string) (*dateTimeFormat, error) {
	requested, err := r.canonicalizeLocaleList(locales)
	if err != nil {
		return nil, err
	}
	opts, err := r.coerceOptions(options, service)
	if err != nil {
		return nil, err
	}
	if err := r.localeMatcherOption(opts); err != nil {
		return nil, err
	}
	calendar, hasCalendar, err := r.stringOpt(opts, "calendar")
	if err != nil {
		return nil, err
	}
	if hasCalendar {
		if !isWellFormedUnicodeType(calendar) {
			return nil, r.rangeError("Invalid calendar : " + calendar)
		}
		if calendar != "gregory" {
			return nil, errRuntimeUnsupported("Intl calendar " + calendar)
		}
	}
	numbering, hasNumbering, err := r.stringOpt(opts, "numberingSystem")
	if err != nil {
		return nil, err
	}
	if hasNumbering {
		if !isWellFormedUnicodeType(numbering) {
			return nil, r.rangeError("Invalid numberingSystem : " + numbering)
		}
		if numbering != "latn" {
			return nil, errRuntimeUnsupported("Intl numberingSystem " + numbering)
		}
	}
	hour12, hasHour12, err := r.boolOption(opts, "hour12")
	if err != nil {
		return nil, err
	}
	hourCycleOpt, err := r.stringOption(opts, "hourCycle", "h11", "h12", "h23", "h24")
	if err != nil {
		return nil, err
	}
	optHourCycle := hourCycleOpt
	if hasHour12 {
		optHourCycle = ""
	}
	loc, err := r.resolveRequestedLocale(requested, map[string][]string{"ca": {"gregory"}, "nu": {"latn"}, "hc": {"h11", "h12", "h23", "h24"}}, "ca", "nu")
	if err != nil {
		return nil, err
	}
	hcDefault := "h12"
	if v := loc.extension["hc"]; v != "" {
		hcDefault = v
	}
	hc := ""
	if optHourCycle == "" {
		hc = loc.extension["hc"]
	} else {
		hc = optHourCycle
	}
	if hasHour12 {
		if hour12 {
			hc = "h12"
			if hcDefault == "h11" || hcDefault == "h12" {
				hc = hcDefault
			}
		} else {
			hc = "h23"
			if hcDefault == "h23" || hcDefault == "h24" {
				hc = hcDefault
			}
		}
	} else if hc == "" {
		hc = hcDefault
	}

	tzValue, err := r.optionValue(opts, "timeZone")
	if err != nil {
		return nil, err
	}
	var zone dtfZone
	var tzName string
	if isUndefined(tzValue) {
		zone, tzName, err = r.defaultZone()
	} else {
		var s string
		if s, err = r.toString(tzValue); err != nil {
			return nil, err
		}
		var ok bool
		zone, tzName, ok, err = lookupZone(s)
		if err == nil && !ok {
			err = r.rangeError("Invalid time zone specified: " + s)
		}
	}
	if err != nil {
		return nil, err
	}

	explicit := false
	hasHour := false
	given := map[string]bool{}
	var skeleton strings.Builder
	for _, comp := range dtfComponents {
		if comp.prop == "timeZoneName" {
			fsd, err := r.numberOption(opts, "fractionalSecondDigits", 1, 3, 0)
			if err != nil {
				return nil, err
			}
			if fsd > 0 {
				explicit = true
				given["fractionalSecondDigits"] = true
			}
			skeleton.WriteString(strings.Repeat("S", fsd))
		}
		v, err := r.stringOption(opts, comp.prop, comp.values...)
		if err != nil {
			return nil, err
		}
		if v == "" {
			continue
		}
		explicit = true
		given[comp.prop] = true
		if comp.prop == "hour" {
			hasHour = true
			c := string(hourCycleChar(hc))
			if v == "2-digit" {
				c += c
			}
			skeleton.WriteString(c)
			continue
		}
		skeleton.WriteString(comp.pattern[v])
	}
	if _, err := r.stringOption(opts, "formatMatcher", "basic", "best fit"); err != nil {
		return nil, err
	}
	dateStyle, err := r.stringOption(opts, "dateStyle", dtfStyles...)
	if err != nil {
		return nil, err
	}
	timeStyle, err := r.stringOption(opts, "timeStyle", dtfStyles...)
	if err != nil {
		return nil, err
	}
	f := &dateTimeFormat{zone: zone, timeZone: tzName, dateStyle: dateStyle, timeStyle: timeStyle}
	gen := enGenerator(hourCycleChar(hcDefault))
	if dateStyle != "" || timeStyle != "" {
		if explicit {
			return nil, r.typeError("Invalid option : option")
		}
		if required == dtfRequiredDate && timeStyle != "" {
			return nil, r.typeError("Invalid option : timeStyle")
		}
		if required == dtfRequiredTime && dateStyle != "" {
			return nil, r.typeError("Invalid option : dateStyle")
		}
		if timeStyle != "" {
			f.hourCycle = hc
		}
		f.pattern = dtfStylePattern(gen, dateStyle, timeStyle, f.hourCycle, loc.extension["hc"] != "")
	} else {
		sk := skeleton.String()
		needDefaults := true
		if required == dtfRequiredDate || required == dtfRequiredAny {
			needDefaults = needDefaults && !given["weekday"] && !given["year"] && !given["month"] && !given["day"]
		}
		if required == dtfRequiredTime || required == dtfRequiredAny {
			needDefaults = needDefaults && !given["dayPeriod"] && !given["hour"] && !given["minute"] && !given["second"] && !given["fractionalSecondDigits"]
		}
		if needDefaults && (defaults == dtfDefaultsDate || defaults == dtfDefaultsAll) {
			sk += "yMd"
		}
		if needDefaults && (defaults == dtfDefaultsTime || defaults == dtfDefaultsAll) {
			switch hc {
			case "h12":
				sk += "hms"
			case "h11":
				sk += "Kms"
			case "h24":
				sk += "kms"
			default:
				sk += "Hms"
			}
		}
		if hasHour {
			f.hourCycle = hc
		}
		f.pattern = replaceHourCycleInPattern(gen.bestPattern(sk), f.hourCycle)
	}
	// hour12/hourCycle options drop a disagreeing -u-hc- from the locale.
	if (hasHour12 || hourCycleOpt != "") && loc.extension["hc"] != "" && loc.extension["hc"] != f.hourCycle {
		loc = loc.without("hc")
	}
	f.locale = loc.locale
	f.items = compileDatePattern(f.pattern)
	for _, it := range f.items {
		switch it.ch {
		case 'm':
			f.hasMinute = true
		case 's':
			f.hasSecond = true
		}
	}
	return f, nil
}

// ICU SimpleDateFormat's time skeletons, used instead of the standard time
// patterns when the locale carries an -u-hc- keyword.
var enTimeSkeletons = [4]string{"jmmsszzzz", "jmmssz", "jmmss", "jmm"}

// dtfStylePattern implements V8's DateTimeStylePattern for "en".
func dtfStylePattern(gen *dtpg, dateStyle, timeStyle, hc string, hcKeyword bool) string {
	index := func(s string) int {
		for i, v := range dtfStyles {
			if v == s {
				return i
			}
		}
		return -1
	}
	timePattern := ""
	if timeStyle != "" {
		timePattern = enDateTimePatterns[index(timeStyle)]
		if hcKeyword {
			timePattern = enGeneratorFor(gen.hourChar, true).bestPatternOpts(enTimeSkeletons[index(timeStyle)], false)
		}
	}
	var pattern string
	switch {
	case dateStyle != "" && timeStyle != "":
		pattern = simpleFormat(enDateAtTime[index(dateStyle)], timePattern, enDateTimePatterns[4+index(dateStyle)])
	case dateStyle != "":
		return enDateTimePatterns[4+index(dateStyle)]
	default:
		pattern = timePattern
	}
	if hc == hourCycleFromPattern(pattern) {
		return pattern
	}
	sk := dtMatch(pattern)
	return replaceHourCycleInPattern(gen.bestPattern(replaceSkeletonHourCycle(sk.skeletonString(), hc)), hc)
}

func hourCycleFromPattern(pattern string) string {
	inQuote := false
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '\'':
			inQuote = !inQuote
		case 'K':
			if !inQuote {
				return "h11"
			}
		case 'h':
			if !inQuote {
				return "h12"
			}
		case 'H':
			if !inQuote {
				return "h23"
			}
		case 'k':
			if !inQuote {
				return "h24"
			}
		}
	}
	return ""
}

func replaceSkeletonHourCycle(skeleton, hc string) string {
	to := hourCycleChar(hc)
	var b strings.Builder
	for i := 0; i < len(skeleton); i++ {
		switch c := skeleton[i]; c {
		case 'a', 'b', 'B':
		case 'h', 'H', 'K', 'k':
			b.WriteByte(to)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// replaceHourCycleInPattern implements V8's ReplaceHourCycleInPattern.
func replaceHourCycleInPattern(pattern, hc string) string {
	to := hourCycleChar(hc)
	if to == 0 {
		return pattern
	}
	var b strings.Builder
	replace := true
	var last byte
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		switch c {
		case '\'':
			replace = !replace
			b.WriteByte(c)
		case 'H', 'h', 'K', 'k':
			if replace && last == 'd' {
				b.WriteByte(' ')
			}
			if replace {
				b.WriteByte(to)
			} else {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
		last = c
	}
	return b.String()
}

// resolveRequestedLocale resolves an already canonicalized locale list.
// Extension keys named in decline must use a supported value; any other
// value would format differently in V8, so the script falls back instead.
func (r *rt) resolveRequestedLocale(requested []string, relevant map[string][]string, decline ...string) (resolvedLocale, error) {
	for _, tag := range requested {
		t, _, _ := parseLanguageTag(tag)
		if _, ok := lookupLocale(t.base); !ok {
			continue
		}
		for _, key := range decline {
			if v, ok := t.ext[key]; ok && !contains(relevant[key], v) {
				return resolvedLocale{}, errRuntimeUnsupported("Intl locale extension -u-" + key + "-" + v)
			}
		}
		break
	}
	return r.resolveLocaleList(requested, relevant), nil
}

func (l resolvedLocale) without(key string) resolvedLocale {
	t, _, _ := parseLanguageTag(l.locale)
	delete(t.ext, key)
	ext := map[string]string{}
	for k, v := range l.extension {
		if k != key {
			ext[k] = v
		}
	}
	return resolvedLocale{locale: t.String(), extension: ext}
}

// --- time zones ---

var (
	localZoneOnce sync.Once
	localZoneName string
)

// systemZoneName finds the IANA name of time.Local ($TZ or /etc/localtime).
func systemZoneName() string {
	localZoneOnce.Do(func() {
		if tz, ok := os.LookupEnv("TZ"); ok {
			tz = strings.TrimPrefix(tz, ":")
			if tz == "" {
				tz = "UTC"
			}
			localZoneName = tz
			return
		}
		if target, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
			if i := strings.Index(target, "zoneinfo/"); i >= 0 {
				localZoneName = target[i+len("zoneinfo/"):]
			}
		}
	})
	return localZoneName
}

// defaultZone is the host's time zone (ExecuteOptions.Location).
func (r *rt) defaultZone() (dtfZone, string, error) {
	loc := r.location()
	name := loc.String()
	if loc == time.Local {
		name = systemZoneName()
	}
	if canonical, ok := tzCanonical(name); ok {
		return dtfZone{id: canonical, loc: loc}, canonical, nil
	}
	return dtfZone{}, "", errRuntimeUnsupported("Intl default time zone " + loc.String())
}

// tzCanonical resolves an ICU-accepted identifier to the one V8 reports.
func tzCanonical(id string) (string, bool) {
	if to, ok := tzAliases[id]; ok {
		return to, true
	}
	if _, ok := tzZoneNames[id]; ok {
		return id, true
	}
	return "", false
}

// lookupZone implements V8's CreateTimeZone and TimeZoneId.
func lookupZone(s string) (dtfZone, string, bool, error) {
	if offset, ok := parseOffsetZone(s); ok {
		return dtfZone{offset: offset}, formatOffsetZoneID(offset), true, nil
	}
	canonical, ok := tzCanonical(canonicalizeTimeZoneID(s))
	if !ok {
		return dtfZone{}, "", false, nil
	}
	loc, err := loadLocation(canonical)
	if err != nil {
		return dtfZone{}, "", false, errRuntimeUnsupported("time zone data for " + canonical)
	}
	return dtfZone{id: canonical, loc: loc}, canonical, true, nil
}

var (
	locationMu    sync.Mutex
	locationCache = map[string]*time.Location{}
)

func loadLocation(id string) (*time.Location, error) {
	locationMu.Lock()
	defer locationMu.Unlock()
	if l := locationCache[id]; l != nil {
		return l, nil
	}
	l, err := time.LoadLocation(id)
	if err != nil {
		for alias, to := range tzAliases {
			if to == id {
				if l, err = time.LoadLocation(alias); err == nil {
					break
				}
			}
		}
	}
	if err != nil {
		return nil, err
	}
	locationCache[id] = l
	return l, nil
}

// parseOffsetZone implements V8's GetOffsetTimeZone: ±HH, ±HHMM, ±HH:MM.
func parseOffsetZone(s string) (int, bool) {
	rs := []rune(s)
	if len(rs) < 3 {
		return 0, false
	}
	sign := 1
	switch rs[0] {
	case '+':
	case '-', '−':
		sign = -1
	default:
		return 0, false
	}
	h0, h1 := rs[1], rs[2]
	if !(h0 >= '0' && h0 <= '1' && h1 >= '0' && h1 <= '9' || h0 == '2' && h1 >= '0' && h1 <= '3') {
		return 0, false
	}
	hours := int(h0-'0')*10 + int(h1-'0')
	if len(rs) == 3 {
		return sign * hours * 3600, true
	}
	p := 3
	if rs[p] == ':' {
		p++
		if p == len(rs) {
			return 0, false
		}
	}
	if len(rs)-p != 2 {
		return 0, false
	}
	m0, m1 := rs[p], rs[p+1]
	if m0 < '0' || m0 > '5' || m1 < '0' || m1 > '9' {
		return 0, false
	}
	return sign * (hours*3600 + (int(m0-'0')*10+int(m1-'0'))*60), true
}

func formatOffsetZoneID(offset int) string {
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	return sign + pad2(offset/3600) + ":" + pad2(offset/60%60)
}

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// canonicalizeTimeZoneID implements V8's JSDateTimeFormat::CanonicalizeTimeZoneID.
func canonicalizeTimeZoneID(input string) string {
	upper := strings.ToUpper(asciiOnly(input))
	switch {
	case len(upper) == 3:
		if upper == "GMT" {
			return "UTC"
		}
		return upper
	case len(upper) == 7 && upper[3] >= '0' && upper[3] <= '9':
		return upper
	case len(upper) > 3:
		switch {
		case strings.HasPrefix(upper, "ETC"):
			if upper == "ETC/UTC" || upper == "ETC/GMT" || upper == "ETC/UCT" {
				return "UTC"
			}
			if strings.HasPrefix(upper, "ETC/GMT") {
				return gmtZoneID(input)
			}
		case strings.HasPrefix(upper, "GMT"):
			if upper == "GMT0" || upper == "GMT+0" || upper == "GMT-0" {
				return "UTC"
			}
		case strings.HasPrefix(upper, "US/"):
			title := titleCaseZone(input)
			if len(title) >= 2 {
				title = title[:1] + "S" + title[2:]
			}
			return title
		case strings.HasPrefix(upper, "SYSTEMV/"):
			return "SystemV/" + upper[8:]
		}
	}
	for _, special := range []string{"America/Argentina/ComodRivadavia", "America/Knox_IN", "Antarctica/DumontDUrville", "Antarctica/McMurdo",
		"Australia/ACT", "Australia/LHI", "Australia/NSW", "Brazil/DeNoronha", "Chile/EasterIsland", "GB", "GB-Eire",
		"Mexico/BajaNorte", "Mexico/BajaSur", "NZ", "NZ-CHAT", "W-SU"} {
		if strings.ToUpper(special) == upper {
			return special
		}
	}
	return titleCaseZone(input)
}

func asciiOnly(s string) string {
	for i := 0; i < len(s); i++ {
		if s[i] >= 0x80 {
			return ""
		}
	}
	return s
}

func gmtZoneID(input string) string {
	switch len(input) {
	case 8:
		if input[7] == '0' {
			return "Etc/GMT0"
		}
	case 9:
		if (input[7] == '+' || input[7] == '-') && input[8] >= '0' && input[8] <= '9' {
			return "Etc/GMT" + input[7:9]
		}
	case 10:
		if (input[7] == '+' || input[7] == '-') && input[8] == '1' && input[9] >= '0' && input[9] <= '4' {
			return "Etc/GMT" + input[7:10]
		}
	}
	return ""
}

// titleCaseZone implements V8's ToTitleCaseTimezoneLocation.
func titleCaseZone(input string) string {
	var b []byte
	word := 0
	for i := 0; i < len(input); i++ {
		ch := input[i]
		switch {
		case ch|0x20 >= 'a' && ch|0x20 <= 'z':
			if word == 0 {
				b = append(b, ch&^0x20)
			} else {
				b = append(b, ch|0x20)
			}
			word++
		case ch == '_' || ch == '-' || ch == '/':
			if word == 2 {
				if sub := string(b[len(b)-2:]); sub == "Of" || sub == "Es" || sub == "Au" {
					b[len(b)-2] |= 0x20
				}
			}
			b = append(b, ch)
			word = 0
		default:
			return ""
		}
	}
	return string(b)
}

// offsetAt returns the zone's UTC offset in seconds at msec.
func (z dtfZone) offsetAt(msec int64) int {
	if z.loc == nil {
		return z.offset
	}
	_, off := timeFromMsec(msec).In(z.loc).Zone()
	return off
}

type tzEntry struct {
	offset                         int
	short, long, shortGen, longGen string
}

type tzRun struct {
	from, to int
	entries  []tzEntry
}

var (
	tzParsedMu sync.Mutex
	tzParsed   = map[string][]tzRun{}
)

func tzRuns(id string) []tzRun {
	tzParsedMu.Lock()
	defer tzParsedMu.Unlock()
	if runs, ok := tzParsed[id]; ok {
		return runs
	}
	var runs []tzRun
	for _, chunk := range strings.Split(tzZoneNames[id], ";") {
		fields := strings.Split(chunk, "|")
		if len(fields) < 6 {
			continue
		}
		years := strings.SplitN(fields[0], "-", 2)
		from, _ := strconv.Atoi(years[0])
		to, _ := strconv.Atoi(years[1])
		run := tzRun{from: from, to: to}
		for i := 1; i+4 < len(fields); i += 5 {
			off, _ := strconv.Atoi(fields[i])
			run.entries = append(run.entries, tzEntry{off, fields[i+1], fields[i+2], fields[i+3], fields[i+4]})
		}
		runs = append(runs, run)
	}
	tzParsed[id] = runs
	return runs
}

// zoneName formats a timeZoneName field.
func (z dtfZone) zoneName(style string, msec int64) (string, error) {
	offset := z.offsetAt(msec)
	switch style {
	case "O", "OOOO":
		if offset == 0 {
			// V8 prints the explicit offset styles with a sign at zero.
			if style == "O" {
				return "GMT+0", nil
			}
			return "GMT+00:00", nil
		}
		return gmtOffsetText(offset, style == "O"), nil
	}
	short := style == "z" || style == "v"
	if z.loc == nil {
		if offset == 0 { // ICU's "GMT" zone
			if short {
				return "GMT", nil
			}
			return "Greenwich Mean Time", nil
		}
		return gmtOffsetText(offset, short), nil
	}
	column := map[string]int{"z": 0, "zzzz": 1, "v": 2, "vvvv": 3}[style]
	t := timeFromMsec(msec)
	if msec < 0 || msec >= metazoneEnd {
		fields := strings.Split(tzZonePre[z.id], "|")
		if t.In(z.loc).IsDST() {
			column += 4
		}
		if column >= len(fields) {
			return "", errRuntimeUnsupported("time zone name for " + z.id + " at " + t.UTC().Format(time.RFC3339))
		}
		switch v := fields[column]; v {
		case "@":
		case "?":
			return "", errRuntimeUnsupported("time zone name for " + z.id + " at " + t.UTC().Format(time.RFC3339))
		default:
			return v, nil
		}
		if offset == 0 {
			if short {
				return "GMT+0", nil
			}
			return "GMT+00:00", nil
		}
		return gmtOffsetText(offset, short), nil
	}
	year := min(t.UTC().Year(), tzLastYear)
	for _, run := range tzRuns(z.id) {
		if year < run.from || year > run.to {
			continue
		}
		found := ""
		for _, e := range run.entries {
			if e.offset != offset {
				continue
			}
			v := e.long
			switch style {
			case "z":
				v = e.short
			case "v":
				v = e.shortGen
			case "vvvv":
				v = e.longGen
			}
			if found != "" && found != v {
				// Standard and daylight time shared this offset that year.
				return "", errRuntimeUnsupported("ambiguous time zone name for " + z.id + " in " + strconv.Itoa(year))
			}
			found = v
		}
		if found != "" {
			return found, nil
		}
	}
	return "", errRuntimeUnsupported("time zone name for " + z.id + " in " + strconv.Itoa(year))
}

// metazoneEnd is where ICU's metazone mappings end (9999-12-31T23:59Z).
const metazoneEnd = 253402300740000

// gmtOffsetText is ICU's localized GMT format ("GMT", "GMT-7", "GMT+05:30").
func gmtOffsetText(offset int, short bool) string {
	if offset == 0 {
		return "GMT"
	}
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	h, m, s := offset/3600, offset/60%60, offset%60
	var b strings.Builder
	b.WriteString("GMT" + sign)
	if short {
		b.WriteString(strconv.Itoa(h))
		if m != 0 || s != 0 {
			b.WriteString(":" + pad2(m))
		}
	} else {
		b.WriteString(pad2(h) + ":" + pad2(m))
	}
	if s != 0 {
		b.WriteString(":" + pad2(s))
	}
	return b.String()
}

// --- formatting ---

type dtfItem struct {
	lit   string
	ch    byte
	count int
}

// compileDatePattern splits a SimpleDateFormat pattern into fields and
// literal runs (” is a quote; quoted text is literal).
func compileDatePattern(pattern string) []dtfItem {
	var items []dtfItem
	var lit strings.Builder
	flush := func() {
		if lit.Len() > 0 {
			items = append(items, dtfItem{lit: lit.String()})
			lit.Reset()
		}
	}
	inQuote := false
	for i := 0; i < len(pattern); i++ {
		c := pattern[i]
		if c == '\'' {
			if i+1 < len(pattern) && pattern[i+1] == '\'' {
				lit.WriteByte('\'')
				i++
				continue
			}
			inQuote = !inQuote
			continue
		}
		if !inQuote && c|0x20 >= 'a' && c|0x20 <= 'z' {
			j := i + 1
			for j < len(pattern) && pattern[j] == c {
				j++
			}
			flush()
			items = append(items, dtfItem{ch: c, count: j - i})
			i = j - 1
			continue
		}
		lit.WriteByte(c)
	}
	flush()
	return items
}

var (
	enMonthsWide   = []string{"January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"}
	enMonthsAbbr   = []string{"Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"}
	enMonthsNarrow = []string{"J", "F", "M", "A", "M", "J", "J", "A", "S", "O", "N", "D"}
	enDaysWide     = []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}
	enDaysAbbr     = []string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}
	enDaysShort    = []string{"Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"}
	enDaysNarrow   = []string{"S", "M", "T", "W", "T", "F", "S"}
)

// calendarFields are the local fields of an instant. V8 sets ICU's
// Gregorian change to -Infinity, so the calendar is proleptic Gregorian.
type calendarFields struct {
	year, month, day, weekday int // month 0-11, weekday 0 = Sunday; year is astronomical
	hour, minute, second, ms  int
}

const msPerDay = 86400000

func localFields(local int64) calendarFields {
	days := floorDiv(local, msPerDay)
	msInDay := local - days*msPerDay
	y, m, d := civilFromDays(days)
	return calendarFields{
		year: y, month: m - 1, day: d,
		hour: int(msInDay / 3600000), minute: int(msInDay / 60000 % 60),
		second: int(msInDay / 1000 % 60), ms: int(msInDay % 1000),
		weekday: int(((days % 7) + 11) % 7), // 1970-01-01 was a Thursday
	}
}

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && (a < 0) != (b < 0) {
		q--
	}
	return q
}

// civilFromDays converts days since 1970-01-01 to a proleptic Gregorian
// date (Howard Hinnant's algorithm), valid for the whole Date range.
func civilFromDays(z int64) (int, int, int) {
	z += 719468
	era := floorDiv(z, 146097)
	doe := z - era*146097
	yoe := (doe - doe/1460 + doe/36524 - doe/146096) / 365
	y := yoe + era*400
	doy := doe - (365*yoe + yoe/4 - yoe/100)
	mp := (5*doy + 2) / 153
	d := doy - (153*mp+2)/5 + 1
	m := mp + 3
	if m > 12 {
		m -= 12
	}
	if m <= 2 {
		y++
	}
	return int(y), int(m), int(d)
}

func zeroPad(n, minDigits int) string {
	s := strconv.Itoa(n)
	if len(s) > 10 {
		s = s[len(s)-10:]
	}
	if len(s) < minDigits {
		s = strings.Repeat("0", minDigits-len(s)) + s
	}
	return s
}

// enDayPeriod implements ICU's flexible day period ('B') for "en"
// (CLDR 48: morning1 00:00-12:00, noon 12:00, afternoon1, evening1 18:00,
// night1 21:00; "midnight" output is suppressed by ICU).
func enDayPeriod(hour, minute, second, count int) string {
	if hour == 12 && minute == 0 && second == 0 {
		if count == 5 {
			return "n"
		}
		return "noon"
	}
	switch {
	case hour < 12:
		return "in the morning"
	case hour < 18:
		return "in the afternoon"
	case hour < 21:
		return "in the evening"
	}
	return "at night"
}

func (f *dateTimeFormat) format(msec int64) ([]intlPart, error) {
	offset := f.zone.offsetAt(msec)
	c := localFields(msec + int64(offset)*1000)
	eraYear, era := c.year, 1
	if c.year <= 0 {
		eraYear, era = 1-c.year, 0
	}
	var parts []intlPart
	for _, it := range f.items {
		if it.ch == 0 {
			if n := len(parts); n > 0 && parts[n-1].typ == "literal" {
				parts[n-1].value += it.lit
			} else {
				parts = append(parts, intlPart{typ: "literal", value: it.lit})
			}
			continue
		}
		n := it.count
		var typ, value string
		switch it.ch {
		case 'G':
			typ = "era"
			switch {
			case n == 5:
				value = []string{"B", "A"}[era]
			case n == 4:
				value = []string{"Before Christ", "Anno Domini"}[era]
			default:
				value = []string{"BC", "AD"}[era]
			}
		case 'y':
			typ = "year"
			if n == 2 {
				value = zeroPad(eraYear%100, 2)
			} else {
				value = zeroPad(eraYear, n)
			}
		case 'M', 'L':
			typ = "month"
			switch n {
			case 5:
				value = enMonthsNarrow[c.month]
			case 4:
				value = enMonthsWide[c.month]
			case 3:
				value = enMonthsAbbr[c.month]
			default:
				value = zeroPad(c.month+1, n)
			}
		case 'd':
			typ, value = "day", zeroPad(c.day, n)
		case 'E', 'c', 'e':
			typ = "weekday"
			switch {
			case n == 5:
				value = enDaysNarrow[c.weekday]
			case n == 4:
				value = enDaysWide[c.weekday]
			case n == 6:
				value = enDaysShort[c.weekday]
			case n < 3 && it.ch != 'E':
				value = zeroPad(c.weekday+1, n)
			default:
				value = enDaysAbbr[c.weekday]
			}
		case 'a':
			typ = "dayPeriod"
			pm := 0
			if c.hour >= 12 {
				pm = 1
			}
			if n == 5 {
				value = []string{"a", "p"}[pm]
			} else {
				value = []string{"AM", "PM"}[pm]
			}
		case 'B':
			typ = "dayPeriod"
			minute, second := 0, 0
			if f.hasMinute {
				minute = c.minute
			}
			if f.hasSecond {
				second = c.second
			}
			value = enDayPeriod(c.hour, minute, second, n)
		case 'h':
			h := c.hour % 12
			if h == 0 {
				h = 12
			}
			typ, value = "hour", zeroPad(h, n)
		case 'H':
			typ, value = "hour", zeroPad(c.hour, n)
		case 'K':
			typ, value = "hour", zeroPad(c.hour%12, n)
		case 'k':
			h := c.hour
			if h == 0 {
				h = 24
			}
			typ, value = "hour", zeroPad(h, n)
		case 'm':
			typ, value = "minute", zeroPad(c.minute, n)
		case 's':
			typ, value = "second", zeroPad(c.second, n)
		case 'S':
			typ = "fractionalSecond"
			v := c.ms
			switch n {
			case 1:
				v /= 100
			case 2:
				v /= 10
			}
			value = zeroPad(v, min(n, 3)) + strings.Repeat("0", max(n-3, 0))
		case 'z', 'O', 'v':
			typ = "timeZoneName"
			style := strings.Repeat(string(it.ch), 1)
			if n >= 4 {
				style = strings.Repeat(string(it.ch), 4)
			}
			var err error
			if value, err = f.zone.zoneName(style, msec); err != nil {
				return nil, err
			}
		default:
			return nil, errRuntimeUnsupported("date pattern field " + string(it.ch))
		}
		parts = append(parts, intlPart{typ: typ, value: value})
	}
	return parts, nil
}

// formatValue implements format / formatToParts on a date argument.
func (f *dateTimeFormat) formatValue(r *rt, v any, toParts bool) (any, error) {
	var x float64
	if isUndefined(v) {
		x = float64(r.now().UnixMilli())
	} else {
		n, err := r.toNumber(v)
		if err != nil {
			return nil, err
		}
		x = n
	}
	if math.IsNaN(x) || math.Abs(x) > 8.64e15 {
		return nil, r.rangeError("Invalid time value")
	}
	parts, err := f.format(int64(math.Trunc(x)))
	if err != nil {
		return nil, err
	}
	if toParts {
		return intlParts(parts), nil
	}
	return joinParts(parts), nil
}

func (f *dateTimeFormat) resolvedOptions() *object {
	o := newObject(12)
	o.set("locale", f.locale)
	o.set("calendar", "gregory")
	o.set("numberingSystem", "latn")
	o.set("timeZone", f.timeZone)
	if f.hourCycle != "" {
		o.set("hourCycle", f.hourCycle)
		o.set("hour12", f.hourCycle == "h11" || f.hourCycle == "h12")
	}
	if f.dateStyle == "" && f.timeStyle == "" {
		for _, item := range dtfResolvedPairs {
			if item.prop == "timeZoneName" {
				if fsd := min(strings.Count(f.pattern, "S"), 3); fsd > 0 {
					o.set("fractionalSecondDigits", float64(fsd))
				}
			}
			for _, pair := range item.pairs {
				if strings.Contains(f.pattern, pair[0]) {
					o.set(item.prop, pair[1])
					break
				}
			}
		}
	}
	if f.dateStyle != "" {
		o.set("dateStyle", f.dateStyle)
	}
	if f.timeStyle != "" {
		o.set("timeStyle", f.timeStyle)
	}
	return o
}

// dateToLocale implements Date.prototype.toLocale{,Date,Time}String when a
// locales or options argument is given.
func (r *rt) dateToLocale(d *dateValue, args []any, required dtfRequired, defaults dtfDefaults, method string) (any, error) {
	if !d.isSet() {
		return "Invalid Date", nil
	}
	f, err := r.newDateTimeFormat(arg(args, 0), arg(args, 1), required, defaults, method)
	if err != nil {
		return nil, err
	}
	parts, err := f.format(d.msec)
	if err != nil {
		return nil, err
	}
	return joinParts(parts), nil
}

// tzCanonicalIDs lists Intl.supportedValuesOf("timeZone").
func tzCanonicalIDs() []string {
	return tzSupported
}
