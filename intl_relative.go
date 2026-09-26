package toolscript

import (
	"math"
	"strings"
)

// Intl.RelativeTimeFormat with CLDR "en" data. Numbers are formatted as
// ICU's RelativeDateTimeFormatter does with the locale's default decimal
// format ("#,##0.###", half-even).

type relativeTimeFormat struct {
	locale string
	style  int // index into the data: 0 long, 1 short, 2 narrow
	auto   bool
}

var rtfNumber = numberFormat{grouping: ugAuto, dig: intlDigits{minInt: 1, maxFrac: 3, increment: 1, mode: rmHalfEven}}

func init() {
	registerIntlKind(&intlKind{
		name:        "RelativeTimeFormat",
		construct:   func(r *rt, locales, options any) (any, error) { return r.newRelativeTimeFormat(locales, options) },
		requiresNew: true,
		methods: map[string]intlMethod{
			"format": {2, func(r *rt, o *intlObject, args []any) (any, error) {
				parts, err := r.relativeParts(o.state.(*relativeTimeFormat), args, "Intl.RelativeTimeFormat.prototype.format")
				if err != nil {
					return nil, err
				}
				return joinParts(parts), nil
			}},
			"formatToParts": {2, func(r *rt, o *intlObject, args []any) (any, error) {
				parts, err := r.relativeParts(o.state.(*relativeTimeFormat), args, "Intl.RelativeTimeFormat.prototype.formatToParts")
				if err != nil {
					return nil, err
				}
				items := make([]any, len(parts))
				for i, p := range parts {
					obj := newObject(3)
					obj.set("type", p.typ)
					obj.set("value", p.value)
					if p.source != "" {
						obj.set("unit", p.source)
					}
					items[i] = obj
				}
				return &array{items: items}, nil
			}},
			"resolvedOptions": {0, func(r *rt, o *intlObject, _ []any) (any, error) {
				f := o.state.(*relativeTimeFormat)
				res := newObject(4)
				res.set("locale", f.locale)
				res.set("style", nfWidthNames[f.style])
				if f.auto {
					res.set("numeric", "auto")
				} else {
					res.set("numeric", "always")
				}
				res.set("numberingSystem", "latn")
				return res, nil
			}},
		},
	})
}

func (r *rt) newRelativeTimeFormat(locales, options any) (*relativeTimeFormat, error) {
	tags, err := r.canonicalizeLocaleList(locales)
	if err != nil {
		return nil, err
	}
	o, err := r.coerceOptions(options, "Intl.RelativeTimeFormat")
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
	f := &relativeTimeFormat{}
	if f.locale, err = r.intlNumberingLocale(tags, nu, hasNu); err != nil {
		return nil, err
	}
	style, err := r.intlEnumOption(o, "style", []string{"long", "short", "narrow"}, 0)
	if err != nil {
		return nil, err
	}
	f.style = style
	numeric, err := r.intlEnumOption(o, "numeric", []string{"always", "auto"}, 0)
	if err != nil {
		return nil, err
	}
	f.auto = numeric == 1
	return f, nil
}

// relativeParts formats; the unit of number parts travels in part.source.
func (r *rt) relativeParts(f *relativeTimeFormat, args []any, method string) ([]intlPart, error) {
	value, err := r.toNumber(arg(args, 0))
	if err != nil {
		return nil, err
	}
	unit, err := r.toString(arg(args, 1))
	if err != nil {
		return nil, err
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return nil, r.rangeError("Value need to be finite number for " + method + "()")
	}
	singular := strings.TrimSuffix(unit, "s")
	data, ok := intlRelativeTimeData[singular]
	if !ok || (singular != unit && singular+"s" != unit) {
		return nil, r.rangeError("Invalid unit argument for " + method + "() '" + unit + "'")
	}
	row := data[f.style]
	if f.auto && value > -2.1 && value < 2.1 {
		// ICU allows a 1% epsilon around -2..2.
		x100 := float64(value * 100)
		var off int32
		if x100 < 0 {
			off = int32(float64(x100 - 0.5))
		} else {
			off = int32(float64(x100 + 0.5))
		}
		phrase := ""
		switch off {
		case -100:
			phrase = row[4]
		case 0:
			phrase = row[5]
		case 100:
			phrase = row[6]
		}
		if phrase != "" {
			return []intlPart{{typ: "literal", value: phrase}}, nil
		}
	}
	past := math.Signbit(value)
	q := decFromFloat(math.Abs(value))
	rtfNumber.process(&q)
	idx := pluralOf(&q)
	if past {
		idx += 2
	}
	before, after, _ := strings.Cut(row[idx], "{0}")
	parts := make([]intlPart, 0, 6)
	if before != "" {
		parts = append(parts, intlPart{typ: "literal", value: before})
	}
	for _, p := range numberParts(&q, 1, nil) {
		p.source = singular
		parts = append(parts, p)
	}
	if after != "" {
		parts = append(parts, intlPart{typ: "literal", value: after})
	}
	return parts, nil
}
