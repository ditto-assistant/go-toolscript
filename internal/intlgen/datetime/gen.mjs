// Generates testdata/intl/datetime.json: Intl.DateTimeFormat scripts with the
// output V8 produces for them. Scripts run as function bodies with the host
// time zone America/New_York, the setting TestIntlDateTimeGolden replays:
//
//   TZ=America/New_York node internal/intlgen/datetime/gen.mjs > testdata/intl/datetime.json
if (process.env.TZ !== "America/New_York") throw new Error("run with TZ=America/New_York");

let seed = 20260926;
const rand = () => { // mulberry32
  seed = (seed + 0x6d2b79f5) | 0;
  let t = Math.imul(seed ^ (seed >>> 15), 1 | seed);
  t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
  return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
};
const pick = (xs) => xs[Math.floor(rand() * xs.length)];
const q = JSON.stringify;

const instants = [
  "Date.UTC(2026, 2, 5, 4, 7, 9, 12)", "Date.UTC(2026, 8, 24, 17, 0, 0, 0)", "Date.UTC(2026, 0, 1, 0, 0, 0, 0)",
  "Date.UTC(2026, 6, 4, 12, 0, 0, 0)", "Date.UTC(2026, 6, 4, 12, 30, 0, 0)", "Date.UTC(2025, 11, 31, 23, 59, 59, 999)",
  "Date.UTC(2026, 2, 8, 7, 30)", "Date.UTC(2026, 10, 1, 6, 30)", "0", "-1", "86400000 * 365.25 * 30",
  "Date.UTC(1999, 11, 31, 23, 0)", "Date.UTC(2000, 1, 29, 18, 45, 1, 5)", "Date.UTC(1969, 6, 20, 20, 17, 40)",
  "Date.UTC(1582, 9, 15)", "Date.UTC(1582, 9, 14, 23)", "Date.UTC(1000, 0, 1, 12)", "Date.UTC(-5, 5, 15)",
  "Date.UTC(0, 0, 1)", "Date.UTC(275760, 8, 13)", "-8.64e15", "Date.UTC(2038, 0, 19, 3, 14, 8)",
  "Date.UTC(2045, 3, 1, 9)", "Date.UTC(1965, 3, 1, 9)", "Date.UTC(2026, 3, 1, 21, 5, 0, 7)",
];
const zones = [undefined, "UTC", "America/Los_Angeles", "America/New_York", "America/Toronto", "Europe/London",
  "Europe/Berlin", "Asia/Kolkata", "Asia/Tokyo", "Australia/Sydney", "Australia/Lord_Howe", "America/Sao_Paulo",
  "Pacific/Honolulu", "America/Anchorage", "Asia/Kathmandu", "Africa/Cairo", "America/Phoenix", "Europe/Dublin",
  "Asia/Shanghai", "America/St_Johns", "+05:30", "-08:00", "+0100", "-03", "US/Pacific", "Asia/Calcutta",
  "america/new_york", "etc/utc", "GMT", "Etc/GMT+5", "EST5EDT", "Pacific/Chatham", "America/Argentina/Buenos_Aires",
  "Europe/Moscow", "Asia/Dubai", "America/Mexico_City", "Pacific/Kiritimati"];
// Only locales V8 also resolves to its "en"/"en-US" data: the engine has no
// other locale data and resolves the rest to English (see intl_test.go).
const locales = [undefined, "en-US", "en", "EN-us", ["en-US", "de"], "en-US-u-hc-h23", "en-US-u-hc-h11",
  "en-u-hc-h24", "en-US-u-ca-gregory", "en-US-u-nu-latn", "en-US-u-hc-h12-ca-gregory", []];
const components = {
  weekday: ["narrow", "short", "long"], era: ["narrow", "short", "long"], year: ["numeric", "2-digit"],
  month: ["numeric", "2-digit", "narrow", "short", "long"], day: ["numeric", "2-digit"],
  dayPeriod: ["narrow", "short", "long"], hour: ["numeric", "2-digit"], minute: ["numeric", "2-digit"],
  second: ["numeric", "2-digit"], fractionalSecondDigits: [1, 2, 3],
  timeZoneName: ["short", "long", "shortOffset", "longOffset", "shortGeneric", "longGeneric"],
};
const styles = ["full", "long", "medium", "short"];

const cases = [];
const add = (name, body) => cases.push({ name, script: body });

// One formatter, several instants, parts and resolved options. The script
// text is TEMPLATE with $LOC, $OPTS and $DATES substituted (the Go test does
// the same substitution).
const TEMPLATE = `try {
  const f = new Intl.DateTimeFormat($LOC, $OPTS);
  const ds = [$DATES];
  return JSON.stringify({ f: ds.map((d) => f.format(d)), p: f.formatToParts(ds[0]), r: f.resolvedOptions() });
} catch (e) { return "ERR " + e.name + ": " + e.message; }`;
function formatterCase(name, loc, opts, dates) {
  const c = { name, loc: loc === undefined ? "undefined" : q(loc), opts: opts === undefined ? "undefined" : q(opts), dates: dates.join(", ") };
  c.script = TEMPLATE.replace("$LOC", c.loc).replace("$OPTS", c.opts).replace("$DATES", c.dates);
  cases.push(c);
}

// Curated: what agents write.
add("prod_longOffset_with_styles", `const t=new Date("2026-09-24T10:00:00-07:00"); try { return {timestamp:t.toISOString(),LA:new Intl.DateTimeFormat("en-US",{timeZone:"America/Los_Angeles",dateStyle:"medium",timeStyle:"short",timeZoneName:"longOffset"}).format(t)}; } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("prod_fixed", `const t=new Date("2026-09-24T10:00:00-07:00"); return {LA:new Intl.DateTimeFormat("en-US",{timeZone:"America/Los_Angeles",dateStyle:"medium",timeStyle:"short"}).format(t),Toronto:new Intl.DateTimeFormat("en-US",{timeZone:"America/Toronto",timeZoneName:"longOffset",year:"numeric",month:"short",day:"numeric",hour:"numeric",minute:"2-digit"}).format(t)};`);
add("agent_meeting_times", `const when = new Date("2026-10-02T15:30:00Z");
const zones = ["America/New_York", "Europe/London", "Asia/Tokyo", "Australia/Sydney"];
return zones.map((timeZone) => timeZone + ": " + new Intl.DateTimeFormat("en-US", { timeZone, weekday: "short", hour: "numeric", minute: "2-digit", timeZoneName: "short" }).format(when));`);
add("agent_parts_lookup", `const parts = new Intl.DateTimeFormat("en-US", { timeZone: "America/Chicago", year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", hour12: false }).formatToParts(new Date(Date.UTC(2026, 8, 1, 5, 4)));
const get = (t) => parts.find((p) => p.type === t).value;
return get("year") + "-" + get("month") + "-" + get("day") + " " + get("hour") + ":" + get("minute");`);
add("agent_toLocaleString_tz", `const d = new Date(Date.UTC(2026, 8, 26, 3, 0));
return [d.toLocaleString("en-US", { timeZone: "America/Los_Angeles" }), d.toLocaleDateString("en-US", { timeZone: "Asia/Tokyo", weekday: "long", month: "long", day: "numeric" }), d.toLocaleTimeString("en-US", { timeZone: "Europe/Paris", hour: "2-digit", minute: "2-digit" }), d.toLocaleDateString(undefined, { dateStyle: "long" }), d.toLocaleTimeString("en", { timeStyle: "short", timeZone: "UTC" })];`);
add("agent_map_bound_format", `const f = new Intl.DateTimeFormat("en-US", { timeZone: "UTC", month: "short", day: "numeric" });
return [0, 1, 2].map((i) => Date.UTC(2026, 0, 30 + i)).map(f.format);`);
add("agent_call_without_new", `return Intl.DateTimeFormat("en-US", { timeZone: "UTC", dateStyle: "full" }).format(new Date(Date.UTC(2026, 1, 1)));`);
add("agent_resolved_timezone", `return new Intl.DateTimeFormat().resolvedOptions().timeZone;`);
add("agent_default_format", `return new Intl.DateTimeFormat().format(new Date(Date.UTC(2026, 6, 4, 2, 0)));`);
add("statics", `return JSON.stringify([Intl.DateTimeFormat.supportedLocalesOf(["en-GB", "en", "en-US"]), Intl.getCanonicalLocales(["EN-us", "en-US-u-HC-H23"]), typeof Intl, Intl.supportedValuesOf("timeZone").length > 400, Intl.supportedValuesOf("timeZone").includes("America/Los_Angeles")]);`);
add("object_behaviour", `const f = new Intl.DateTimeFormat("en-US", { timeZone: "UTC" });
return JSON.stringify([String(f), Object.keys(f), JSON.stringify(f), f.format === f.format, typeof f.format, f.format.length, f.formatToParts.length, Object.prototype.toString.call(f)]);`);
add("invalid_date_toLocale", `return [new Date(NaN).toLocaleString("en-US"), new Date(NaN).toLocaleDateString("en-US", { timeZone: "UTC" })];`);

// Errors.
for (const [name, loc, opts] of [
  ["err_style_and_component", "en-US", { dateStyle: "short", weekday: "long" }],
  ["err_style_and_tzname", "en-US", { timeStyle: "short", timeZoneName: "short" }],
  ["err_bad_tz", "en-US", { timeZone: "Mars/Base" }], ["err_empty_tz", "en-US", { timeZone: "" }],
  ["err_bad_tag", "en_US", {}], ["err_bad_tag_list", ["en-US", "!!"], {}], ["err_bad_hour", "en-US", { hour: "bogus" }],
  ["err_bad_hc", "en-US", { hourCycle: "h13" }], ["err_bad_style", "en-US", { dateStyle: "huge" }],
  ["err_fsd_range", "en-US", { fractionalSecondDigits: 4 }], ["err_fsd_zero", "en-US", { fractionalSecondDigits: 0 }],
  ["err_fsd_nan", "en-US", { fractionalSecondDigits: "x" }], ["err_matcher", "en-US", { localeMatcher: "x" }],
  ["err_calendar_malformed", "en-US", { calendar: "!!" }], ["err_nu_malformed", "en-US", { numberingSystem: "!" }],
  ["err_format_matcher", "en-US", { formatMatcher: "x" }], ["err_tz_null", "en-US", { timeZone: null }],
]) formatterCase(name, loc, opts, ["0"]);
add("err_options_null", `try { new Intl.DateTimeFormat("en-US", null); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("err_format_nan", `try { return new Intl.DateTimeFormat("en-US").format(NaN); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("err_format_range", `try { return new Intl.DateTimeFormat("en-US").format(8.64e15 + 1); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("err_receiver", `try { return new Intl.DateTimeFormat().formatToParts.call({}, 0); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("err_toLocaleDate_timeStyle", `try { return new Date(0).toLocaleDateString("en-US", { timeStyle: "short" }); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("err_toLocaleTime_dateStyle", `try { return new Date(0).toLocaleTimeString("en-US", { dateStyle: "short" }); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("err_gcl", `try { return Intl.getCanonicalLocales("xx-!!"); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("err_gcl_type", `try { return Intl.getCanonicalLocales([5]); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
add("err_supported_values", `try { return Intl.supportedValuesOf("x"); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);

// Every style combination with each hour cycle setting.
for (const ds of [undefined, ...styles]) for (const ts of [undefined, ...styles]) {
  if (!ds && !ts) continue;
  for (const hc of [{}, { hour12: false }, { hour12: true }, { hourCycle: "h23" }, { hourCycle: "h11" }, { hourCycle: "h24" }]) {
    formatterCase(`style_${ds}_${ts}_${q(hc)}`, "en-US", { dateStyle: ds, timeStyle: ts, timeZone: pick(zones.slice(1)), ...hc },
      [instants[0], instants[1], instants[3], instants[8]]);
  }
}
// Every single component value alone, and with hour cycles.
for (const [k, vs] of Object.entries(components)) for (const v of vs) for (const hc of [{}, { hour12: false }, { hourCycle: "h11" }]) {
  formatterCase(`single_${k}_${v}_${q(hc)}`, "en-US", { [k]: v, timeZone: "America/Los_Angeles", ...hc }, [instants[0], instants[1], instants[3], instants[4]]);
}
// Time zone names across zones and years.
for (const tz of zones) for (const tzn of components.timeZoneName) {
  formatterCase(`zone_${tz}_${tzn}`, "en-US", { timeZone: tz, timeZoneName: tzn, hour: "numeric" }, [instants[0], instants[3], instants[9], instants[11], instants[13], instants[21]]);
}
// Random combinations.
for (let i = 0; i < 2000; i++) {
  const opts = {};
  for (const [k, vs] of Object.entries(components)) if (rand() < 0.3) opts[k] = pick(vs);
  const r = rand();
  if (r < 0.15) opts.hour12 = rand() < 0.5;
  else if (r < 0.3) opts.hourCycle = pick(["h11", "h12", "h23", "h24"]);
  const tz = pick(zones);
  if (tz !== undefined) opts.timeZone = tz;
  const dates = [pick(instants), pick(instants), pick(instants)];
  formatterCase(`random_${i}`, pick(locales), opts, dates);
}
// toLocale*String with arguments.
for (let i = 0; i < 300; i++) {
  const method = pick(["toLocaleString", "toLocaleDateString", "toLocaleTimeString"]);
  const opts = {};
  for (const [k, vs] of Object.entries(components)) if (rand() < 0.15) opts[k] = pick(vs);
  if (rand() < 0.2) opts.dateStyle = pick(styles);
  if (rand() < 0.2) opts.timeStyle = pick(styles);
  if (rand() < 0.2) opts.hour12 = rand() < 0.5;
  opts.timeZone = pick(zones);
  add(`toLocale_${i}`, `try { return new Date(${pick(instants)}).${method}(${q(pick(locales)) ?? "undefined"}, ${q(opts)}); } catch (e) { return "ERR " + e.name + ": " + e.message; }`);
}

for (const c of cases) {
  try {
    const v = new Function(c.script)();
    c.want = typeof v === "string" ? v : JSON.stringify(v);
  } catch (e) {
    c.want = "UNCAUGHT " + e.name + ": " + e.message;
  }
}
// One case per line keeps the file diffable; template cases omit the script.
const lines = cases.map((c) => JSON.stringify(c.loc === undefined ? c : { name: c.name, loc: c.loc, opts: c.opts, dates: c.dates, want: c.want }));
process.stdout.write(`{"node": ${q(process.version)}, "icu": ${q(process.versions.icu)}, "tz": ${q(process.env.TZ)}, "template": ${q(TEMPLATE)}, "cases": [\n${lines.join(",\n")}\n]}\n`);
