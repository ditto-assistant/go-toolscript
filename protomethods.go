package toolscript

import (
	"sync"
)

// Built-in methods read as values (arr.map, d.getTime, re.test) are the
// shared prototype functions of JavaScript: one function per owner and name,
// identical across instances (`[].map === [].map`), operating on `this`.
// Calling one detached, or on the wrong receiver, fails like Goja does.

var (
	protoFnsOnce sync.Once
	protoFns     map[string]*function
)

// Spec function lengths (Goja reports the same).
var methodLengths = map[string]int{
	"Array.at": 1, "Array.concat": 1, "Array.copyWithin": 2, "Array.every": 1, "Array.fill": 1, "Array.filter": 1,
	"Array.find": 1, "Array.findIndex": 1, "Array.findLast": 1, "Array.findLastIndex": 1, "Array.flatMap": 1,
	"Array.forEach": 1, "Array.includes": 1, "Array.indexOf": 1, "Array.join": 1, "Array.lastIndexOf": 1,
	"Array.map": 1, "Array.push": 1, "Array.reduce": 1, "Array.reduceRight": 1, "Array.slice": 2, "Array.some": 1,
	"Array.sort": 1, "Array.splice": 2, "Array.toSorted": 1, "Array.toSpliced": 2, "Array.unshift": 1, "Array.with": 2,
	"String.at": 1, "String.charAt": 1, "String.charCodeAt": 1, "String.codePointAt": 1, "String.concat": 1,
	"String.endsWith": 1, "String.includes": 1, "String.indexOf": 1, "String.lastIndexOf": 1, "String.localeCompare": 1,
	"String.match": 1, "String.matchAll": 1, "String.padEnd": 1, "String.padStart": 1, "String.repeat": 1,
	"String.replace": 2, "String.replaceAll": 2, "String.search": 1, "String.slice": 2, "String.split": 2,
	"String.startsWith": 1, "String.substr": 2, "String.substring": 2,
	"Number.toExponential": 1, "Number.toFixed": 1, "Number.toPrecision": 1, "Number.toString": 1,
	"Date.setMilliseconds": 1, "Date.setUTCMilliseconds": 1, "Date.setSeconds": 2, "Date.setUTCSeconds": 2,
	"Date.setMinutes": 3, "Date.setUTCMinutes": 3, "Date.setHours": 4, "Date.setUTCHours": 4, "Date.setDate": 1,
	"Date.setUTCDate": 1, "Date.setMonth": 2, "Date.setUTCMonth": 2, "Date.setFullYear": 3, "Date.setUTCFullYear": 3,
	"Date.setTime": 1, "Date.toJSON": 1,
	"RegExp.test": 1, "RegExp.exec": 1,
	"Set.add": 1, "Set.has": 1, "Set.delete": 1, "Set.forEach": 1,
	"Map.get": 1, "Map.set": 2, "Map.has": 1, "Map.delete": 1, "Map.forEach": 1,
}

func protoFn(owner, key string) *function {
	protoFnsOnce.Do(buildProtoFns)
	if f, ok := protoFns[owner+"."+key]; ok {
		return f
	}
	return nil
}

func buildProtoFns() {
	protoFns = map[string]*function{}
	add := func(owner, key string) {
		id := owner + "." + key
		protoFns[id] = &function{name: key, length: methodLengths[id], native: func(r *rt, this any, args []any) (any, error) {
			return r.callProto(owner, key, this, args)
		}}
	}
	for key := range arrayMethods {
		add("Array", key)
	}
	for key := range declinedArrayMembers {
		add("Array", key)
	}
	for key := range stringMethods {
		add("String", key)
	}
	for key := range declinedStringMembers {
		add("String", key)
	}
	for key := range numberMethods {
		add("Number", key)
	}
	for key := range declinedNumberMembers {
		add("Number", key)
	}
	for _, key := range []string{"toString", "valueOf"} {
		add("Boolean", key)
	}
	for key := range dateMethods {
		add("Date", key)
	}
	for _, key := range []string{"test", "exec", "toString"} {
		add("RegExp", key)
	}
	for _, key := range []string{"add", "has", "delete", "clear", "keys", "values", "entries", "forEach"} {
		add("Set", key)
	}
	for _, key := range []string{"get", "set", "has", "delete", "clear", "keys", "values", "entries", "forEach"} {
		add("Map", key)
	}
	for _, key := range []string{"next", "toString"} {
		add("Iterator", key)
	}
}

// callProto applies a built-in prototype method to an explicit receiver.
func (r *rt) callProto(owner, key string, this any, args []any) (any, error) {
	nullishText := func() string {
		if this == nil {
			return "null"
		}
		return "undefined"
	}
	switch owner {
	case "Array":
		a, ok := this.(*array)
		if !ok {
			if isNullish(this) {
				return nil, r.typeError("Cannot convert undefined or null to object")
			}
			return nil, errRuntimeUnsupported("Array.prototype." + key + " on a non-array")
		}
		if m := arrayMethods[key]; m != nil {
			return m(r, a, args)
		}
	case "String":
		if isNullish(this) {
			return nil, r.typeError("Value is not object coercible")
		}
		s, err := r.toString(this)
		if err != nil {
			return nil, err
		}
		if m := stringMethods[key]; m != nil {
			return m(r, s, args)
		}
	case "Number":
		f, ok := this.(float64)
		if !ok {
			s, _ := r.toString(this)
			return nil, r.typeError("Value is not a number: " + s)
		}
		if m := numberMethods[key]; m != nil {
			return m(r, f, args)
		}
	case "Boolean":
		b, ok := this.(bool)
		if !ok {
			return nil, errRuntimeUnsupported("Boolean.prototype." + key + " on a non-boolean")
		}
		return r.callBuiltinMethod(b, key, args)
	case "Date":
		d, ok := this.(*dateValue)
		if !ok {
			if isNullish(this) {
				return nil, r.typeError("Value is not an object: " + nullishText())
			}
			return nil, r.typeError("Method Date.prototype." + key + " is called on incompatible receiver")
		}
		return dateMethods[key](r, d, args)
	case "RegExp":
		if rx, ok := this.(*regexpValue); ok {
			return r.regexpMethod(rx, key, args)
		}
	case "Set", "Map":
		if c, ok := this.(*collection); ok && c.isMap == (owner == "Map") {
			return r.collectionMethod(c, key, args)
		}
	case "Iterator":
		if it, ok := this.(*iterator); ok {
			return r.iteratorMethod(it, key)
		}
	}
	if isNullish(this) {
		return nil, r.typeError("Value is not an object: " + nullishText())
	}
	return nil, errRuntimeUnsupported(owner + ".prototype." + key + " on an incompatible receiver")
}
