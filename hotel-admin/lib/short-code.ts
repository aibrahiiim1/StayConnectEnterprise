// The short code of a package or service plan is an internal identifier the operator never needs to choose. It is made
// from the name (upper case, letters and digits, words joined by "-") plus a random suffix, so two packages with the
// same name still get different codes. A code is fixed once saved; an edit keeps the existing one.

const MAX_BASE = 16;

export function generateShortCode(name: string, random: () => number = Math.random): string {
  const base = name
    .normalize("NFKD")
    .replace(/[̀-ͯ]/g, "")
    .toUpperCase()
    .replace(/[^A-Z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, MAX_BASE)
    .replace(/-+$/g, "");
  const alphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789";
  let suffix = "";
  for (let i = 0; i < 4; i++) suffix += alphabet[Math.floor(random() * alphabet.length) % alphabet.length];
  return `${base || "PKG"}-${suffix}`;
}
