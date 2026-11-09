/**
 * The timezones a routine is offered: one per real timezone, not one per IANA name. Two names
 * are the same timezone when their clocks agree all year — the same offset in January and in
 * July — so New York and Toronto are one row, and each row is the place most people know it by.
 */

/** Zone is one row: an IANA name, what to call it, and where it is now relative to UTC. */
export type Zone = {
  id: string;
  /** name is the timezone's own name, "Eastern Time", or the city when it has none. */
  name: string;
  /** places are the cities it is known by, for reading and for search. */
  places: string;
  /** minutes is the offset from UTC right now, which the list is sorted by. */
  minutes: number;
};

/**
 * COMMON is the place each timezone is known by, in the order to prefer when two of them share
 * a clock. A timezone with no entry here is not offered unless the person is in it.
 */
const COMMON: [id: string, name: string, places: string][] = [
  ["Pacific/Pago_Pago", "Samoa Time", "Pago Pago"],
  ["Pacific/Honolulu", "Hawaii Time", "Honolulu"],
  ["America/Anchorage", "Alaska Time", "Anchorage"],
  ["America/Los_Angeles", "Pacific Time", "Los Angeles, San Francisco, Vancouver, Seattle"],
  ["America/Phoenix", "Arizona", "Phoenix"],
  ["America/Denver", "Mountain Time", "Denver, Calgary, Edmonton"],
  ["America/Chicago", "Central Time", "Chicago, Dallas, Winnipeg"],
  ["America/Mexico_City", "Central, no DST", "Mexico City, Regina, Guatemala City"],
  ["America/New_York", "Eastern Time", "New York, Toronto, Montreal, Miami"],
  ["America/Bogota", "Colombia", "Bogotá, Lima, Quito"],
  ["America/Halifax", "Atlantic Time", "Halifax"],
  ["America/Caracas", "Venezuela", "Caracas"],
  ["America/Santiago", "Chile", "Santiago"],
  ["America/St_Johns", "Newfoundland", "St. John's"],
  ["America/Sao_Paulo", "Brasília Time", "São Paulo, Rio de Janeiro"],
  ["America/Argentina/Buenos_Aires", "Argentina", "Buenos Aires"],
  ["Atlantic/Azores", "Azores", "Ponta Delgada"],
  ["UTC", "UTC", "Coordinated Universal Time"],
  ["Europe/London", "UK & Ireland", "London, Dublin, Lisbon"],
  ["Africa/Casablanca", "Morocco", "Casablanca"],
  ["Europe/Paris", "Central Europe", "Paris, Berlin, Madrid, Rome, Amsterdam"],
  ["Africa/Lagos", "West Africa", "Lagos, Kinshasa"],
  ["Europe/Athens", "Eastern Europe", "Athens, Kyiv, Helsinki, Bucharest"],
  ["Africa/Cairo", "Egypt", "Cairo"],
  ["Africa/Johannesburg", "South Africa", "Johannesburg, Cape Town"],
  ["Asia/Jerusalem", "Israel", "Jerusalem, Tel Aviv"],
  ["Europe/Istanbul", "Turkey", "Istanbul"],
  ["Europe/Moscow", "Moscow", "Moscow, Saint Petersburg"],
  ["Asia/Riyadh", "Arabia", "Riyadh, Doha, Nairobi"],
  ["Asia/Tehran", "Iran", "Tehran"],
  ["Asia/Dubai", "Gulf", "Dubai, Abu Dhabi"],
  ["Asia/Kabul", "Afghanistan", "Kabul"],
  ["Asia/Karachi", "Pakistan", "Karachi, Tashkent"],
  ["Asia/Kolkata", "India", "Mumbai, Delhi, Bengaluru"],
  ["Asia/Kathmandu", "Nepal", "Kathmandu"],
  ["Asia/Dhaka", "Bangladesh", "Dhaka"],
  ["Asia/Yangon", "Myanmar", "Yangon"],
  ["Asia/Bangkok", "Indochina", "Bangkok, Jakarta, Ho Chi Minh City"],
  ["Asia/Shanghai", "China", "Beijing, Shanghai, Hong Kong, Singapore, Taipei"],
  ["Australia/Perth", "Western Australia", "Perth"],
  ["Asia/Tokyo", "Japan & Korea", "Tokyo, Seoul"],
  ["Australia/Darwin", "Northern Territory", "Darwin"],
  ["Australia/Adelaide", "South Australia", "Adelaide"],
  ["Australia/Brisbane", "Queensland", "Brisbane"],
  ["Australia/Sydney", "Eastern Australia", "Sydney, Melbourne, Canberra"],
  ["Pacific/Noumea", "New Caledonia", "Nouméa"],
  ["Pacific/Auckland", "New Zealand", "Auckland, Wellington"],
  ["Pacific/Fiji", "Fiji", "Suva"],
  ["Pacific/Tongatapu", "Tonga", "Nukuʻalofa"],
  ["Pacific/Kiritimati", "Line Islands", "Kiritimati"],
];

/** minutesAt is zone's offset from UTC at when, in minutes; NaN for a name this browser does not know. */
function minutesAt(zone: string, when: Date): number {
  try {
    const part =
      new Intl.DateTimeFormat("en-US", { timeZone: zone, timeZoneName: "shortOffset" })
        .formatToParts(when)
        .find((p) => p.type === "timeZoneName")?.value ?? "";
    const m = /^GMT(?:([+-])(\d{1,2})(?::(\d{2}))?)?$/.exec(part);
    if (!m) return NaN;
    if (!m[1]) return 0;
    const n = Number(m[2]) * 60 + Number(m[3] ?? 0);
    return m[1] === "-" ? -n : n;
  } catch {
    return NaN;
  }
}

/** clock is what makes two names one timezone: their offsets in January and in July. */
function clock(zone: string, year: number): string {
  return `${minutesAt(zone, new Date(Date.UTC(year, 0, 15)))}/${minutesAt(zone, new Date(Date.UTC(year, 6, 15)))}`;
}

/** formatOffset says an offset as people write it: "UTC", "UTC-5", "UTC+5:30". */
export function formatOffset(minutes: number): string {
  if (!Number.isFinite(minutes)) return "";
  if (minutes === 0) return "UTC";
  const a = Math.abs(minutes);
  const h = Math.floor(a / 60);
  const m = a % 60;
  return `UTC${minutes < 0 ? "-" : "+"}${h}${m ? `:${String(m).padStart(2, "0")}` : ""}`;
}

/** city is the place an IANA name ends in: "America/Argentina/Buenos_Aires" → "Buenos Aires". */
export function city(zone: string): string {
  return (zone.split("/").pop() ?? zone).replaceAll("_", " ");
}

/**
 * commonZones is the list a person picks from, sorted by offset: one row per timezone in
 * COMMON, with the person's own zone and the one already chosen standing in for the row of
 * the timezone they belong to, so someone in Toronto sees Toronto rather than New York.
 */
export function commonZones(own: string[], now = new Date()): Zone[] {
  const year = now.getUTCFullYear();
  const rows = new Map<string, Zone>();
  for (const [id, name, places] of COMMON) {
    const minutes = minutesAt(id, now);
    if (Number.isNaN(minutes)) continue;
    const key = clock(id, year);
    if (!rows.has(key)) rows.set(key, { id, name, places, minutes });
  }
  for (const id of own) {
    if (!id) continue;
    const minutes = minutesAt(id, now);
    if (Number.isNaN(minutes)) continue;
    const key = clock(id, year);
    const row = rows.get(key);
    if (row?.id === id) continue;
    // Its timezone's row, named for this place; or a row of its own when no common place
    // shares its clock.
    rows.set(key, row ? { ...row, id, places: city(id) } : { id, name: city(id), places: id, minutes });
  }
  return [...rows.values()].sort((a, b) => a.minutes - b.minutes || a.name.localeCompare(b.name));
}

/** zoneLabel is a chosen zone as the trigger shows it: "Eastern Time · Toronto". */
export function zoneLabel(zone: Zone | undefined, id: string): string {
  if (!zone) return city(id);
  if (zone.id === "UTC") return "UTC";
  const place = zone.places.split(",")[0]?.trim() ?? city(id);
  return place === zone.name ? place : `${zone.name} · ${place}`;
}
