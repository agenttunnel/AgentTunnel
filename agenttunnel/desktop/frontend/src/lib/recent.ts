// Recently opened places (threads and projects), newest first, so the palette
// can lead with where the user just was. Per viewer: kept in localStorage,
// never synced, and a failed read is just an empty list.

const KEY = "at.recent";
const MAX = 12;

let cache: string[] | null = null;

function load(): string[] {
  if (cache) return cache;
  try {
    const raw = JSON.parse(localStorage.getItem(KEY) ?? "[]");
    cache = Array.isArray(raw) ? raw.filter((x): x is string => typeof x === "string") : [];
  } catch {
    cache = [];
  }
  return cache;
}

/** Ids are palette item ids: "thread:<id>" or "project:<id>". */
export function recordRecent(id: string) {
  const list = load();
  if (list[0] === id) return;
  cache = [id, ...list.filter((x) => x !== id)].slice(0, MAX);
  try {
    localStorage.setItem(KEY, JSON.stringify(cache));
  } catch {
    /* private mode: the list lasts this session */
  }
}

export function recentIds(): string[] {
  return load();
}
