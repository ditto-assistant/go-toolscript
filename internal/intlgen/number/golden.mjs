// Generates the golden corpus for Intl.NumberFormat, Intl.PluralRules,
// Intl.ListFormat and Intl.RelativeTimeFormat under testdata/intl/number.
//
//   node internal/intlgen/number/golden.mjs
//
// With arguments it writes a larger, differently seeded corpus elsewhere
// for local soak runs (not committed):
//
//   node internal/intlgen/number/golden.mjs /tmp/intl-soak 17 10
//   TOOLSCRIPT_INTL_GOLDEN=/tmp/intl-soak go test -run TestIntlNumberGolden
//
// Every case is a small script body; Node runs it here and records what
// it returns (always a string: JSON.stringify of the results, with errors
// rendered as "Name: message"). TestIntlNumberGolden runs the same bodies
// through the engine and compares the strings exactly. Cases are
// pseudo-random but seeded, so the files are reproducible.
import { writeFileSync, mkdirSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "../../..");
const outDir = process.argv[2] || join(root, "testdata/intl/number");
const seedOffset = Number(process.argv[3] || 0);
const scale = Number(process.argv[4] || 1);
mkdirSync(outDir, { recursive: true });

function mulberry32(a) {
  return function () {
    a |= 0;
    a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}
let rand = mulberry32(1 + seedOffset);
const pick = (xs) => xs[Math.floor(rand() * xs.length)];
const chance = (p) => rand() < p;
const int = (lo, hi) => lo + Math.floor(rand() * (hi - lo + 1));

// JS source for a value.
function src(v) {
  if (typeof v === "string") return JSON.stringify(v);
  if (v === undefined) return "undefined";
  if (Object.is(v, -0)) return "-0";
  if (typeof v === "number") return Number.isNaN(v) ? "NaN" : v === Infinity ? "Infinity" : v === -Infinity ? "-Infinity" : String(v);
  if (Array.isArray(v)) return "[" + v.map(src).join(", ") + "]";
  if (v === null) return "null";
  if (typeof v === "object") return "{" + Object.entries(v).map(([k, x]) => `${JSON.stringify(k)}: ${src(x)}`).join(", ") + "}";
  return String(v);
}

const ERR = `(e) => e.name + ": " + e.message`;
function run(body) {
  return new Function(body)();
}
function tryAll(expr) {
  return `(() => { try { return ${expr}; } catch (e) { return e.name + ": " + e.message; } })()`;
}

const files = {};
function add(file, name, body) {
  const want = run(body);
  if (typeof want !== "string") throw new Error("case must return a string: " + body);
  (files[file] ||= []).push({ name, code: body, want });
}

// ---------------------------------------------------------------- values
const units = Intl.supportedValuesOf("unit");
const currencies = Intl.supportedValuesOf("currency");
const edgeNumbers = [0, -0, 1, -1, 2, 0.5, -0.5, 1.5, 2.5, -2.5, 0.125, 0.05, 0.005, 1.005, 1.25, 9.995, 99.95, 999.5, 999.95, 999.995, 1000, 1234.5678, -1234.5678, 9999, 99999, 999999, 1e6, 1234567.891, 1e21, 1.5e21, 2 ** 53, 2 ** 70, 1e100, 1.7976931348623157e308, 5e-324, 1e-7, 1.23e-7, 0.1 + 0.2, 0.30000000000000004, 123456789.987654321, 0.000123456, 12.345, 1.45, 1.55, 2.675, 0.045, 99999.5, 123456789012345680000, NaN, Infinity, -Infinity];
const edgeStrings = ["1.2345678901234567890123", "-0", "  42 ", "0x1F", "0b101", "0o17", "1e3", "abc", "", "123456789012345678901234567890.5", "-9.995", ".5", "5.", "+1.25e2", "1e-400", "1e400", "-1e400", "0.000000000000000000000001", "1_000", "Infinity", "-Infinity", "0x1fffffffffffffffff", "999999.9999999999999999", "12345678901234567890"];
function randomNumber() {
  switch (int(0, 6)) {
    case 0: return int(-100, 100);
    case 1: return Math.round((rand() * 2 - 1) * 10 ** int(0, 8)) / 10 ** int(0, 4);
    case 2: return (rand() * 2 - 1) * 10 ** int(-10, 25);
    case 3: return int(0, 1) ? int(1, 9) * 10 ** int(0, 16) : -int(1, 9) * 10 ** int(0, 16);
    case 4: return Number((rand() * 1000).toFixed(int(0, 5)));
    case 5: return int(0, 99) + pick([0.5, 0.05, 0.005, 0.995, 0.9995, 0.25, 0.125, 0.375, 0.45, 0.55]);
    default: return pick(edgeNumbers);
  }
}
function randomValue() {
  const r = rand();
  if (r < 0.7) return randomNumber();
  if (r < 0.95) return pick(edgeStrings.concat([String(randomNumber()), (rand() * 1e6).toFixed(int(0, 12))]));
  return pick([true, false, null, undefined, [5], [1, 2], {}]);
}

// ---------------------------------------------------------------- NumberFormat
const locales = [undefined, "en", "en-US", ["en-US"], ["en", "en-US"], "en-u-nu-latn", "en-US-u-nu-latn", [], "EN-us", "en-US-u-ca-gregory"];
function randomUnit() {
  if (chance(0.03)) return pick(["foo", "meter-per", "kilometer-per-hour-per-second", "Meter", "", "meters", "percent-per-percent"]);
  if (chance(0.3)) return pick(units) + "-per-" + pick(units);
  return pick(units);
}
function randomCurrency() {
  if (chance(0.03)) return pick(["US", "USDX", "U$D", "", "12A", "ÜSD"]);
  if (chance(0.5)) return pick(["USD", "EUR", "JPY", "GBP", "CAD", "INR", "CHF", "KWD", "XXX", "ABC", "usd", "eur", "BTC", "CLF", "XAU", "AUD", "MXN", "CNY", "KRW", "BYR"]);
  return pick(currencies);
}
function randomOptions(kind) {
  const o = {};
  const style = kind === "nf" ? pick([undefined, undefined, "decimal", "percent", "currency", "currency", "unit", "unit"]) : undefined;
  if (chance(0.3) && kind === "nf") o.localeMatcher = pick(["lookup", "best fit", "best fit", "other"]);
  if (style !== undefined) o.style = style;
  if (chance(0.005) && kind === "nf") o.style = "foo";
  if (style === "currency" || (kind === "nf" && chance(0.05))) {
    if (!chance(0.03)) o.currency = randomCurrency();
    if (chance(0.5)) o.currencyDisplay = pick(["symbol", "narrowSymbol", "code", "name", chance(0.05) ? "long" : "name"]);
    if (chance(0.4)) o.currencySign = pick(["standard", "accounting", "accounting", chance(0.05) ? "x" : "standard"]);
  }
  if (style === "unit" || (kind === "nf" && chance(0.05))) {
    if (!chance(0.03)) o.unit = randomUnit();
    if (chance(0.6)) o.unitDisplay = pick(["short", "narrow", "long", chance(0.05) ? "full" : "long"]);
  }
  if (chance(0.35)) o.notation = pick(["standard", "scientific", "engineering", "compact", "compact", chance(0.02) ? "x" : "compact"]);
  if (chance(0.25)) o.compactDisplay = pick(["short", "long"]);
  if (chance(0.1)) o.minimumIntegerDigits = chance(0.03) ? pick([0, 22, "x", 1.9]) : int(1, 21);
  const digitMode = rand();
  if (digitMode < 0.35) {
    if (chance(0.6)) o.minimumFractionDigits = chance(0.03) ? pick([-1, 101, NaN]) : int(0, chance(0.8) ? 6 : 100);
    if (chance(0.6)) o.maximumFractionDigits = chance(0.03) ? pick([-1, 101, "20"]) : int(0, chance(0.8) ? 8 : 100);
  } else if (digitMode < 0.55) {
    if (chance(0.6)) o.minimumSignificantDigits = chance(0.03) ? pick([0, 22]) : int(1, chance(0.8) ? 6 : 21);
    if (chance(0.7)) o.maximumSignificantDigits = chance(0.03) ? pick([0, 22]) : int(1, 21);
  } else if (digitMode < 0.7) {
    if (chance(0.5)) o.minimumFractionDigits = int(0, 5);
    if (chance(0.5)) o.maximumFractionDigits = int(0, 8);
    if (chance(0.5)) o.minimumSignificantDigits = int(1, 5);
    if (chance(0.5)) o.maximumSignificantDigits = int(1, 10);
    o.roundingPriority = pick(["auto", "morePrecision", "lessPrecision", chance(0.05) ? "x" : "auto"]);
  }
  if (chance(0.12)) {
    o.roundingIncrement = chance(0.05) ? pick([3, 0, 5001, 7]) : pick([1, 2, 5, 10, 20, 25, 50, 100, 200, 250, 500, 1000, 2000, 2500, 5000]);
    if (chance(0.7)) {
      const f = int(0, 4);
      o.minimumFractionDigits = f;
      o.maximumFractionDigits = f;
    }
  }
  if (chance(0.3)) o.roundingMode = pick(["ceil", "floor", "expand", "trunc", "halfCeil", "halfFloor", "halfExpand", "halfTrunc", "halfEven", chance(0.02) ? "up" : "halfEven"]);
  if (chance(0.15)) o.trailingZeroDisplay = pick(["auto", "stripIfInteger", chance(0.05) ? "strip" : "stripIfInteger"]);
  if (kind === "nf") {
    if (chance(0.25)) o.useGrouping = pick([true, false, "always", "auto", "min2", "true", "false", "", 0, 1, null, chance(0.1) ? "x" : "min2"]);
    if (chance(0.3)) o.signDisplay = pick(["auto", "never", "always", "exceptZero", "negative", chance(0.02) ? "x" : "always"]);
    if (chance(0.03)) o.numberingSystem = pick(["latn", "abcd", "a", "latn-x", "arab"]);
  }
  if (kind === "pr" && chance(0.5)) o.type = pick(["cardinal", "ordinal", chance(0.05) ? "x" : "ordinal"]);
  return o;
}

function nfCase(i, loc, opts, values) {
  const L = src(loc);
  const O = src(opts);
  const V = values.map(src).join(", ");
  const pairs = [];
  for (let k = 0; k < 3; k++) pairs.push(`[${src(pick(values))}, ${src(pick(values))}]`);
  const body = [
    `let nf;`,
    `try { nf = new Intl.NumberFormat(${L}, ${O}); } catch (e) { return e.name + ": " + e.message; }`,
    `const vals = [${V}];`,
    `const f = nf.format;`,
    `return JSON.stringify({`,
    `  resolved: nf.resolvedOptions(),`,
    `  format: vals.map((v) => ${tryAll("f(v)")}),`,
    `  parts: vals.slice(0, 4).map((v) => ${tryAll("nf.formatToParts(v)")}),`,
    `  range: [${pairs.join(", ")}].map((p) => ${tryAll("nf.formatRange(p[0], p[1])")}),`,
    `  rangeParts: [${pairs[0]}].map((p) => ${tryAll("nf.formatRangeToParts(p[0], p[1])")}),`,
    `});`,
  ].join("\n");
  add("numberformat", `nf-${i}`, body);
}

rand = mulberry32(20260926 + seedOffset);
for (let i = 0; i < 3000 * scale; i++) {
  const loc = pick(locales);
  const opts = randomOptions("nf");
  const values = [];
  for (let k = 0; k < 10; k++) values.push(randomValue());
  nfCase(i, loc, opts, values);
}

// Exhaustive-ish sweeps over the dimensions that are data-driven.
rand = mulberry32(7 + seedOffset);
let n = 0;
for (const c of currencies.concat(["XXX", "ABC", "DEM", "usd", "BYR", "ITL"])) {
  for (const cd of ["symbol", "narrowSymbol", "code", "name"]) {
    nfCase(`cur-${n++}`, "en", { style: "currency", currency: c, currencyDisplay: cd, currencySign: pick(["standard", "accounting"]) }, [1, -1, 0, 1234.567, -0.001, 1e6, NaN, -Infinity]);
  }
}
for (const u of units) {
  for (const ud of ["short", "narrow", "long"]) {
    nfCase(`unit-${n++}`, "en", { style: "unit", unit: u, unitDisplay: ud }, [1, -1, 0, 2, 1.5, 1000, -1234.5, NaN]);
    nfCase(`unit-${n++}`, "en", { style: "unit", unit: u, unitDisplay: ud, notation: pick(["compact", "scientific"]) }, [1, 1500, -2e6, 0.5]);
  }
}
for (const a of units) {
  for (const b of units) {
    if (!chance(0.15) && !(a === "percent" || b === "percent")) continue;
    nfCase(`per-${n++}`, "en", { style: "unit", unit: `${a}-per-${b}`, unitDisplay: pick(["short", "narrow", "long"]), notation: pick(["standard", "standard", "compact"]) }, [1, 2, -1.5, 12345]);
  }
}
for (let e = -12; e <= 24; e++) {
  for (const cd of ["short", "long"]) {
    const vals = [1, 1.5, 9.99, 9.995, 1.2345, 99.999].map((m) => m * 10 ** e);
    nfCase(`compact-${n++}`, "en", { notation: "compact", compactDisplay: cd }, vals.concat(vals.map((v) => -v)));
    nfCase(`compact-${n++}`, "en", { notation: "compact", compactDisplay: cd, style: "currency", currency: pick(["USD", "EUR", "CAD"]), currencyDisplay: pick(["symbol", "code", "name"]) }, vals);
    nfCase(`sci-${n++}`, "en", { notation: pick(["scientific", "engineering"]), maximumFractionDigits: int(0, 4) }, vals.concat(vals.map((v) => -v)));
  }
}

// ---------------------------------------------------------------- toLocaleString
rand = mulberry32(99 + seedOffset);
for (let i = 0; i < 400 * scale; i++) {
  const v = randomNumber();
  // Without locales and options the engine keeps Goja's Number#toString
  // behavior, so every case passes at least one of them.
  const opts = chance(0.8) ? randomOptions("nf") : undefined;
  const loc = opts === undefined ? pick(["en", "en-US", ["en"]]) : pick([undefined, "en", "en-US", ["en"]]);
  const expr = `(${src(v)}).toLocaleString(${src(loc)}, ${src(opts)})`;
  add("tolocalestring", `tls-${i}`, `return JSON.stringify(${tryAll(expr)});`);
}

// ---------------------------------------------------------------- curated agent usage
const curated = [
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).format(1234.5)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", maximumFractionDigits: 0 }).format(1234.5)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "EUR" }).format(-42)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", currencySign: "accounting" }).format(-42)`,
  `new Intl.NumberFormat("en-US", { style: "percent" }).format(0.256)`,
  `new Intl.NumberFormat("en-US", { style: "percent", minimumFractionDigits: 1 }).format(0.256)`,
  `new Intl.NumberFormat("en-US", { style: "percent", maximumFractionDigits: 2, signDisplay: "exceptZero" }).format(-0.0123)`,
  `new Intl.NumberFormat("en", { notation: "compact" }).format(1234)`,
  `new Intl.NumberFormat("en", { notation: "compact" }).format(1500000)`,
  `new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 }).format(987654321)`,
  `new Intl.NumberFormat("en", { notation: "compact", compactDisplay: "long" }).format(2500000000)`,
  `new Intl.NumberFormat().format(1234567.891)`,
  `new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 }).format(3.14159)`,
  `new Intl.NumberFormat("en-US", { minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(5)`,
  `Intl.NumberFormat("en-US").format(0.1 + 0.2)`,
  `new Intl.NumberFormat("en-US", { style: "unit", unit: "kilobyte" }).format(2048)`,
  `new Intl.NumberFormat("en-US", { style: "unit", unit: "megabyte", unitDisplay: "long", maximumFractionDigits: 1 }).format(12.345)`,
  `new Intl.NumberFormat("en-US", { style: "unit", unit: "kilometer-per-hour" }).format(88)`,
  `new Intl.NumberFormat("en-US", { style: "unit", unit: "millisecond", unitDisplay: "narrow" }).format(350)`,
  `new Intl.NumberFormat("en-US", { style: "unit", unit: "day", unitDisplay: "long" }).format(1)`,
  `(1234.5).toLocaleString("en-US")`,
  `(1234.5).toLocaleString("en-US", { style: "currency", currency: "USD" })`,
  `(0.075).toLocaleString(undefined, { style: "percent", minimumFractionDigits: 1 })`,
  `(123456789).toLocaleString("en")`,
  `(42).toLocaleString("en-US", { minimumIntegerDigits: 3 })`,
  `new Intl.NumberFormat("en", { maximumSignificantDigits: 3 }).format(123456)`,
  `new Intl.NumberFormat("en", { notation: "scientific" }).format(123456)`,
  `new Intl.NumberFormat("en", { notation: "engineering" }).format(123456)`,
  `new Intl.NumberFormat("en", { signDisplay: "always" }).format(5)`,
  `new Intl.NumberFormat("en", { useGrouping: false }).format(1234567)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "JPY" }).format(5000)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", currencyDisplay: "name" }).format(1)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", currencyDisplay: "code" }).format(99.99)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).formatToParts(-1234.56)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "USD" }).formatRange(10, 20)`,
  `new Intl.NumberFormat("en-US", { maximumFractionDigits: 0 }).formatRange(2.9, 3.1)`,
  `new Intl.NumberFormat("en-US").resolvedOptions()`,
  `[1, 22, 333, 4444].map(new Intl.NumberFormat("en-US").format)`,
  `new Intl.NumberFormat("en-US", { roundingMode: "floor", maximumFractionDigits: 0 }).format(-2.5)`,
  `new Intl.NumberFormat("en-US", { roundingIncrement: 5, minimumFractionDigits: 2, maximumFractionDigits: 2 }).format(1.23)`,
  `new Intl.NumberFormat("en-US", { trailingZeroDisplay: "stripIfInteger", minimumFractionDigits: 2 }).format(5)`,
  `new Intl.NumberFormat("en-US", { style: "currency", currency: "USD", notation: "compact" }).format(1234567)`,
  `Intl.NumberFormat.supportedLocalesOf(["en-US", "en"])`,
  `new Intl.NumberFormat("en-US").format("1234567890123456789.12345")`,
  `new Intl.NumberFormat("en-US", { style: "percent" }).format("0.1234")`,
  `new Intl.PluralRules("en-US").select(1)`,
  `new Intl.PluralRules("en-US").select(2)`,
  `new Intl.PluralRules("en-US", { type: "ordinal" }).select(22)`,
  `[1, 2, 3, 4, 11, 12, 13, 21, 22, 23, 101, 111].map((n) => new Intl.PluralRules("en", { type: "ordinal" }).select(n))`,
  `new Intl.PluralRules("en").resolvedOptions()`,
  `new Intl.ListFormat("en", { style: "long", type: "conjunction" }).format(["a", "b", "c"])`,
  `new Intl.ListFormat("en", { type: "disjunction" }).format(["red", "green", "blue"])`,
  `new Intl.ListFormat("en").format(["Alice", "Bob"])`,
  `new Intl.ListFormat("en").format([])`,
  `new Intl.ListFormat("en").format(["solo"])`,
  `new Intl.ListFormat("en", { style: "short" }).format(["x", "y", "z"])`,
  `new Intl.ListFormat("en", { style: "narrow", type: "unit" }).format(["3 ft", "7 in"])`,
  `new Intl.ListFormat("en").formatToParts(["a", "b", "c"])`,
  `new Intl.RelativeTimeFormat("en").format(-3, "day")`,
  `new Intl.RelativeTimeFormat("en", { numeric: "auto" }).format(-1, "day")`,
  `new Intl.RelativeTimeFormat("en", { numeric: "auto" }).format(1, "week")`,
  `new Intl.RelativeTimeFormat("en", { numeric: "auto" }).format(0, "year")`,
  `new Intl.RelativeTimeFormat("en", { style: "short" }).format(5, "minutes")`,
  `new Intl.RelativeTimeFormat("en", { style: "narrow" }).format(-2, "hours")`,
  `new Intl.RelativeTimeFormat("en").formatToParts(100, "day")`,
  `new Intl.RelativeTimeFormat("en").resolvedOptions()`,
  `new Intl.RelativeTimeFormat("en").format(-1.5, "months")`,
  `new Intl.RelativeTimeFormat().format(2, "quarter")`,
];
curated.forEach((expr, i) => add("curated", `curated-${i}`, `return JSON.stringify(${tryAll(expr)});`));

// ---------------------------------------------------------------- constructor errors and statics
const errorExprs = [
  `new Intl.NumberFormat("en", { style: "currency" })`,
  `new Intl.NumberFormat("en", { style: "currency", currency: "US" })`,
  `new Intl.NumberFormat("en", { style: "unit" })`,
  `new Intl.NumberFormat("en", { style: "unit", unit: "parsec" })`,
  `new Intl.NumberFormat("en", { style: "bogus" })`,
  `new Intl.NumberFormat("en", { maximumFractionDigits: 101 })`,
  `new Intl.NumberFormat("en", { minimumFractionDigits: 3, maximumFractionDigits: 1 })`,
  `new Intl.NumberFormat("en", { minimumSignificantDigits: 5, maximumSignificantDigits: 2 })`,
  `new Intl.NumberFormat("en", { roundingIncrement: 3 })`,
  `new Intl.NumberFormat("en", { roundingIncrement: 5 })`,
  `new Intl.NumberFormat("en", { roundingIncrement: 5, maximumSignificantDigits: 2 })`,
  `new Intl.NumberFormat("en", { useGrouping: "sometimes" })`,
  `new Intl.NumberFormat("en", { numberingSystem: "x" })`,
  `new Intl.NumberFormat("en", { localeMatcher: "nope" })`,
  `new Intl.NumberFormat("en", null)`,
  `new Intl.NumberFormat("not a locale")`,
  `new Intl.NumberFormat("en", { notation: "compact", compactDisplay: "tiny" })`,
  `new Intl.NumberFormat("en", { signDisplay: "sometimes" })`,
  `new Intl.NumberFormat("en", { roundingMode: "up" })`,
  `new Intl.NumberFormat("en", { minimumIntegerDigits: 0 })`,
  `new Intl.NumberFormat("en").formatRange(undefined, 1)`,
  `new Intl.NumberFormat("en").formatRange(1)`,
  `new Intl.NumberFormat("en").formatRange(NaN, 1)`,
  `new Intl.NumberFormat("en").formatRange(1, "x")`,
  `new Intl.NumberFormat("en").formatRange({}, 1)`,
  `new Intl.NumberFormat("en").formatRange([1, 2], 1)`,
  `new Intl.NumberFormat("en").format()`,
  `new Intl.NumberFormat("en").formatToParts()`,
  `new Intl.NumberFormat("en", "abc").resolvedOptions()`,
  `new Intl.NumberFormat("en", 5).resolvedOptions()`,
  `(5).toLocaleString("en", { style: "currency" })`,
  `(5).toLocaleString("en", { style: "percent", maximumFractionDigits: -1 })`,
  `(5).toLocaleString("xx-invalid-locale-tag-1234567890")`,
  `Intl.PluralRules("en")`,
  `new Intl.PluralRules("en", { type: "x" })`,
  `new Intl.PluralRules("en").selectRange(undefined, 1)`,
  `new Intl.PluralRules("en").selectRange(1)`,
  `new Intl.PluralRules("en").selectRange(NaN, 1)`,
  `new Intl.PluralRules("en").selectRange(1, NaN)`,
  `Intl.ListFormat("en")`,
  `new Intl.ListFormat("en", { type: "x" })`,
  `new Intl.ListFormat("en", { style: "x" })`,
  `new Intl.ListFormat("en").format(["a", 1])`,
  `new Intl.ListFormat("en").format([null])`,
  `new Intl.ListFormat("en").format([undefined, "a"])`,
  `new Intl.ListFormat("en").format([{}])`,
  `new Intl.ListFormat("en").format([["a"]])`,
  `new Intl.ListFormat("en").format([true])`,
  `new Intl.ListFormat("en").format(5)`,
  `new Intl.ListFormat("en").format(null)`,
  `new Intl.ListFormat("en").format({})`,
  `new Intl.ListFormat("en").format(true)`,
  `new Intl.ListFormat("en").format("abc")`,
  `new Intl.ListFormat("en").format()`,
  `new Intl.ListFormat("en").formatToParts(["a", -0])`,
  `Intl.RelativeTimeFormat("en")`,
  `new Intl.RelativeTimeFormat("en", { style: "x" })`,
  `new Intl.RelativeTimeFormat("en", { numeric: "x" })`,
  `new Intl.RelativeTimeFormat("en", { numberingSystem: "x" })`,
  `new Intl.RelativeTimeFormat("en").format(1, "decade")`,
  `new Intl.RelativeTimeFormat("en").format(1, "Day")`,
  `new Intl.RelativeTimeFormat("en").format(NaN, "day")`,
  `new Intl.RelativeTimeFormat("en").format(Infinity, "day")`,
  `new Intl.RelativeTimeFormat("en").format(1)`,
  `new Intl.RelativeTimeFormat("en").format("x", "bogus")`,
  `new Intl.RelativeTimeFormat("en").formatToParts(NaN, "day")`,
  `Intl.NumberFormat.supportedLocalesOf("en")`,
  `Intl.PluralRules.supportedLocalesOf(["en-US", "EN"])`,
  `Intl.ListFormat.supportedLocalesOf(["en"])`,
  `Intl.RelativeTimeFormat.supportedLocalesOf(["en"])`,
  `Intl.supportedValuesOf("currency")`,
  `Intl.supportedValuesOf("unit")`,
  `typeof new Intl.NumberFormat().format`,
  `new Intl.NumberFormat().format.length`,
  `new Intl.NumberFormat().format === new Intl.NumberFormat().format`,
  `(() => { const nf = new Intl.NumberFormat(); return nf.format === nf.format; })()`,
  `Object.keys(new Intl.NumberFormat().resolvedOptions())`,
  `String(new Intl.NumberFormat())`,
];
errorExprs.forEach((expr, i) => add("errors", `errors-${i}`, `return JSON.stringify(${tryAll(expr)});`));

// ---------------------------------------------------------------- PluralRules
rand = mulberry32(4242 + seedOffset);
const prValues = () => {
  const vs = [];
  for (let k = 0; k < 16; k++) vs.push(chance(0.5) ? int(0, 125) : randomNumber());
  return vs.concat([1, 2, 3, 11, 12, 13, 21, 22, 23, 101, 111, 1.0, 1.5, 0, -1, -0, NaN, 1000, 1e6, "1", "2.00"]);
};
for (let i = 0; i < 700 * scale; i++) {
  const loc = pick(locales);
  const opts = randomOptions("pr");
  const V = prValues().map(src).join(", ");
  const pairs = [];
  for (let k = 0; k < 4; k++) pairs.push(`[${src(pick([0, 1, 2, 3, 11, 1.5, -1, randomNumber()]))}, ${src(pick([0, 1, 2, 3, 22, 1.5, randomNumber()]))}]`);
  const body = [
    `let pr;`,
    `try { pr = new Intl.PluralRules(${src(loc)}, ${src(opts)}); } catch (e) { return e.name + ": " + e.message; }`,
    `return JSON.stringify({`,
    `  resolved: pr.resolvedOptions(),`,
    `  select: [${V}].map((v) => ${tryAll("pr.select(v)")}),`,
    `  range: [${pairs.join(", ")}].map((p) => ${tryAll("pr.selectRange(p[0], p[1])")}),`,
    `});`,
  ].join("\n");
  add("pluralrules", `pr-${i}`, body);
}

// ---------------------------------------------------------------- ListFormat
rand = mulberry32(555 + seedOffset);
const words = ["a", "b", "c", "apple", "", " ", "x y", "日本", "🙂", "Alice", "Bob", "Carol", "d, e"];
for (const type of [undefined, "conjunction", "disjunction", "unit"]) {
  for (const style of [undefined, "long", "short", "narrow"]) {
    for (const loc of [undefined, "en", "en-US", "en-u-nu-arab"]) {
      const lists = [];
      for (let k = 0; k <= 6; k++) {
        const l = [];
        for (let j = 0; j < k; j++) l.push(pick(words));
        lists.push(l);
      }
      const opts = {};
      if (type) opts.type = type;
      if (style) opts.style = style;
      const body = [
        `const lf = new Intl.ListFormat(${src(loc)}, ${src(opts)});`,
        `const lists = ${src(lists)};`,
        `return JSON.stringify({ resolved: lf.resolvedOptions(), format: lists.map((l) => lf.format(l)), parts: lists.map((l) => lf.formatToParts(l)), str: lf.format("xyz") });`,
      ].join("\n");
      add("listformat", `lf-${type}-${style}-${src(loc)}`, body);
    }
  }
}

// ---------------------------------------------------------------- RelativeTimeFormat
rand = mulberry32(777 + seedOffset);
const rtUnits = ["second", "seconds", "minute", "minutes", "hour", "hours", "day", "days", "week", "weeks", "month", "months", "quarter", "quarters", "year", "years"];
for (let i = 0; i < 400 * scale; i++) {
  const opts = {};
  if (chance(0.6)) opts.style = pick(["long", "short", "narrow"]);
  if (chance(0.6)) opts.numeric = pick(["always", "auto"]);
  if (chance(0.05)) opts.numberingSystem = pick(["latn", "abcd", "arab"]);
  if (chance(0.3)) opts.localeMatcher = pick(["lookup", "best fit"]);
  const loc = pick([undefined, "en", "en-US", "en-u-nu-latn", ["en"]]);
  const vals = [];
  for (let k = 0; k < 12; k++) vals.push(chance(0.4) ? int(-3, 3) : chance(0.5) ? pick([-0, 0, 0.004, -0.004, 0.996, -1.004, 1.01, -2, 2, 2.1, -2.099, 1.2345, 1.2355, 1000, 1e21, -1e-7, 12345.6789, "3", "x"]) : randomNumber());
  const body = [
    `let rtf;`,
    `try { rtf = new Intl.RelativeTimeFormat(${src(loc)}, ${src(opts)}); } catch (e) { return e.name + ": " + e.message; }`,
    `const vals = ${src(vals)};`,
    `const units = ${src(rtUnits.filter(() => chance(0.4)).concat([pick(rtUnits)]))};`,
    `return JSON.stringify({`,
    `  resolved: rtf.resolvedOptions(),`,
    `  format: units.map((u) => vals.map((v) => ${tryAll("rtf.format(v, u)")})),`,
    `  parts: units.map((u) => vals.slice(0, 5).map((v) => ${tryAll("rtf.formatToParts(v, u)")})),`,
    `});`,
  ].join("\n");
  add("relativetime", `rtf-${i}`, body);
}

let total = 0;
for (const [file, cases] of Object.entries(files)) {
  total += cases.length;
  const json = JSON.stringify({ node: process.version, icu: process.versions.icu, cases }, null, 1) + "\n";
  writeFileSync(join(outDir, `${file}.json.gz`), gzipSync(json, { level: 9 }));
}
console.log(`wrote ${total} cases to ${outDir}`);
