package toolscript

// Intl.ListFormat with CLDR "en" list patterns.

type listFormat struct {
	locale string
	typ    int // 0 conjunction, 1 disjunction, 2 unit
	style  int // 0 long, 1 short, 2 narrow
}

var (
	listTypeNames  = []string{"conjunction", "disjunction", "unit"}
	listStyleNames = []string{"long", "short", "narrow"}
)

// listPatterns[type][style] = {two, start/middle, end} separators.
var listPatterns = [3][3][3]string{
	{{" and ", ", ", ", and "}, {" & ", ", ", ", & "}, {", ", ", ", ", "}},
	{{" or ", ", ", ", or "}, {" or ", ", ", ", or "}, {" or ", ", ", ", or "}},
	{{", ", ", ", ", "}, {", ", ", ", ", "}, {" ", " ", " "}},
}

func init() {
	registerIntlKind(&intlKind{
		name:        "ListFormat",
		construct:   func(r *rt, locales, options any) (any, error) { return r.newListFormat(locales, options) },
		requiresNew: true,
		methods: map[string]intlMethod{
			"format": {1, func(r *rt, o *intlObject, args []any) (any, error) {
				parts, err := r.listParts(o.state.(*listFormat), arg(args, 0))
				if err != nil {
					return nil, err
				}
				return joinParts(parts), nil
			}},
			"formatToParts": {1, func(r *rt, o *intlObject, args []any) (any, error) {
				parts, err := r.listParts(o.state.(*listFormat), arg(args, 0))
				if err != nil {
					return nil, err
				}
				return intlParts(parts), nil
			}},
			"resolvedOptions": {0, func(r *rt, o *intlObject, _ []any) (any, error) {
				lf := o.state.(*listFormat)
				res := newObject(3)
				res.set("locale", lf.locale)
				res.set("type", listTypeNames[lf.typ])
				res.set("style", listStyleNames[lf.style])
				return res, nil
			}},
		},
	})
}

func (r *rt) newListFormat(locales, options any) (*listFormat, error) {
	tags, err := r.canonicalizeLocaleList(locales)
	if err != nil {
		return nil, err
	}
	o, err := r.coerceOptions(options, "Intl.ListFormat")
	if err != nil {
		return nil, err
	}
	if err := r.localeMatcherOption(o); err != nil {
		return nil, err
	}
	res, err := r.resolveLocale(stringArray(tags), nil)
	if err != nil {
		return nil, err
	}
	lf := &listFormat{locale: res.locale}
	if lf.typ, err = r.intlEnumOption(o, "type", listTypeNames, 0); err != nil {
		return nil, err
	}
	if lf.style, err = r.intlEnumOption(o, "style", listStyleNames, 0); err != nil {
		return nil, err
	}
	return lf, nil
}

// stringListFromIterable implements StringListFromIterable with V8's errors.
func (r *rt) stringListFromIterable(v any) ([]string, error) {
	var notIterable string
	switch t := v.(type) {
	case undefined:
		return nil, nil
	case *array, string, *collection:
	case nil:
		notIterable = "object null"
	case float64:
		notIterable = "number " + numberToString(t)
	case bool:
		if t {
			notIterable = "boolean true"
		} else {
			notIterable = "boolean false"
		}
	case *object, *hostObject, *dateValue, *regexpValue, *intlObject:
		notIterable = "object"
	default:
		return nil, errRuntimeUnsupported("Intl.ListFormat input")
	}
	if notIterable != "" {
		return nil, r.typeError(notIterable + " is not iterable (cannot read property Symbol(Symbol.iterator))")
	}
	var out []string
	_, _, err := r.iterate(v, func(item any) (ctl, any, error) {
		s, ok := item.(string)
		if !ok {
			text, err := r.intlValueText(item)
			if err != nil {
				return 0, nil, err
			}
			return 0, nil, r.typeError("Iterable yielded " + text + " which is not a string")
		}
		out = append(out, s)
		return ctlNormal, nil, nil
	})
	return out, err
}

func (r *rt) listParts(lf *listFormat, v any) ([]intlPart, error) {
	items, err := r.stringListFromIterable(v)
	if err != nil {
		return nil, err
	}
	pat := listPatterns[lf.typ][lf.style]
	parts := make([]intlPart, 0, 2*len(items))
	for i, s := range items {
		if i > 0 {
			sep := pat[1]
			switch {
			case len(items) == 2:
				sep = pat[0]
			case i == len(items)-1:
				sep = pat[2]
			}
			if n := len(parts); n > 0 && parts[n-1].typ == "literal" {
				parts[n-1].value += sep // an empty element leaves one literal run
			} else {
				parts = append(parts, intlPart{typ: "literal", value: sep})
			}
		}
		if s != "" {
			parts = append(parts, intlPart{typ: "element", value: s})
		}
	}
	return parts, nil
}
