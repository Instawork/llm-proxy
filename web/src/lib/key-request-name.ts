// Mirrors apikeys.SlugifyKeyName in internal/apikeys/keyrequests.go.
export const KEY_REQUEST_NAME_MAX = 40;

export function slugifyKeyName(input: string): string {
  let out = "";
  let prevHyphen = false;
  for (const ch of input.toLowerCase()) {
    if (/[a-z0-9]/.test(ch)) {
      out += ch;
      prevHyphen = false;
    } else if (!prevHyphen && out.length > 0) {
      out += "-";
      prevHyphen = true;
    }
  }
  out = out.replace(/^-+|-+$/g, "");
  if (out.length > KEY_REQUEST_NAME_MAX) {
    out = out.slice(0, KEY_REQUEST_NAME_MAX).replace(/-+$/, "");
  }
  return out;
}
