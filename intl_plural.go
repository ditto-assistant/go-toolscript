package toolscript

import "math"

// Intl.PluralRules for English (CLDR "en"): the number is first rounded by
// the digit options (as ICU formats it), then the rules run on ICU's
// operands of the formatted quantity.

type pluralRules struct {
	locale  string
	ordinal bool
	nf      numberFormat // decimal formatter carrying notation and digits
}

var (
	pluralCardinal = []string{"one", "other"}
	pluralOrdinal  = []string{"one", "two", "few", "other"}
)

func init() {
	registerIntlKind(&intlKind{
		name:        "PluralRules",
		construct:   func(r *rt, locales, options any) (any, error) { return r.newPluralRules(locales, options) },
		requiresNew: true,
		methods: map[string]intlMethod{
			"select": {1, func(r *rt, o *intlObject, args []any) (any, error) {
				f, err := r.toNumber(arg(args, 0))
				if err != nil {
					return nil, err
				}
				return o.state.(*pluralRules).selectNumber(f), nil
			}},
			"selectRange": {2, func(r *rt, o *intlObject, args []any) (any, error) {
				start, end := arg(args, 0), arg(args, 1)
				if isUndefined(start) {
					return nil, r.typeError("Invalid startRange : undefined")
				}
				if isUndefined(end) {
					return nil, r.typeError("Invalid endRange : undefined")
				}
				x, err := r.toNumber(start)
				if err != nil {
					return nil, err
				}
				y, err := r.toNumber(end)
				if err != nil {
					return nil, err
				}
				if math.IsNaN(x) {
					return nil, r.rangeError("Invalid startRange : NaN")
				}
				if math.IsNaN(y) {
					return nil, r.rangeError("Invalid endRange : NaN")
				}
				// English plural ranges resolve every pair to "other".
				return "other", nil
			}},
			"resolvedOptions": {0, func(r *rt, o *intlObject, _ []any) (any, error) {
				return o.state.(*pluralRules).resolvedOptions(), nil
			}},
		},
	})
}

func (r *rt) newPluralRules(locales, options any) (*pluralRules, error) {
	tags, err := r.canonicalizeLocaleList(locales)
	if err != nil {
		return nil, err
	}
	o, err := r.coerceOptions(options, "Intl.PluralRules")
	if err != nil {
		return nil, err
	}
	if err := r.localeMatcherOption(o); err != nil {
		return nil, err
	}
	pr := &pluralRules{}
	typ, err := r.intlEnumOption(o, "type", []string{"cardinal", "ordinal"}, 0)
	if err != nil {
		return nil, err
	}
	pr.ordinal = typ == 1
	if pr.nf.notation, err = r.intlEnumOption(o, "notation", nfNotationNames, ntStandard); err != nil {
		return nil, err
	}
	compact, err := r.intlEnumOption(o, "compactDisplay", []string{"short", "long"}, 0)
	if err != nil {
		return nil, err
	}
	pr.nf.compactLong = compact == 1
	// ICU has plural rules for "en" but not "en-US": a requested English
	// locale resolves to "en"; the default locale stays "en-US".
	pr.locale = intlDefaultLocale
	for _, tag := range tags {
		t, _, _ := parseLanguageTag(tag)
		if _, ok := lookupLocale(t.base); ok {
			pr.locale = "en"
			break
		}
	}
	if pr.nf.dig, err = r.intlDigitOptions(o, 0, 3, pr.nf.notation == ntCompact); err != nil {
		return nil, err
	}
	return pr, nil
}

func (pr *pluralRules) selectNumber(f float64) string {
	q := decFromFloat(f)
	pr.nf.process(&q)
	if !pr.ordinal {
		return pluralCardinal[pluralOf(&q)]
	}
	n := math.Abs(q.float())
	if n != math.Floor(n) {
		return "other"
	}
	switch mod10, mod100 := math.Mod(n, 10), math.Mod(n, 100); {
	case mod10 == 1 && mod100 != 11:
		return "one"
	case mod10 == 2 && mod100 != 12:
		return "two"
	case mod10 == 3 && mod100 != 13:
		return "few"
	}
	return "other"
}

func (pr *pluralRules) resolvedOptions() *object {
	o := newObject(12)
	o.set("locale", pr.locale)
	if pr.ordinal {
		o.set("type", "ordinal")
	} else {
		o.set("type", "cardinal")
	}
	o.set("notation", nfNotationNames[pr.nf.notation])
	if pr.nf.notation == ntCompact {
		if pr.nf.compactLong {
			o.set("compactDisplay", "long")
		} else {
			o.set("compactDisplay", "short")
		}
	}
	d := &pr.nf.dig
	o.set("minimumIntegerDigits", float64(d.minInt))
	// V8 reports either the significant or the fraction digits.
	d.resolved(o, d.roundingType == rtFraction, d.roundingType != rtFraction)
	cats := pluralCardinal
	if pr.ordinal {
		cats = pluralOrdinal
	}
	o.set("pluralCategories", stringArray(cats))
	d.roundingTail(o, false)
	return o
}
