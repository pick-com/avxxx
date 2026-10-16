// Plural forms in UI strings (lib/i18n.tsx t(); scripts/i18n-check.mjs).
//
// A value may hold ICU-style plural blocks next to plain {placeholders}:
//
//   "{n, plural, one {# video} other {# videos}}"
//   "{count, plural, one {# release you reserved is out} other {# releases you reserved are out}}"
//
// The branch is picked with Intl.PluralRules for the UI language; `=N`
// selectors (=0 {...}) match an exact number first, and `other` is required
// as the fallback. Inside a branch `#` is the variable's value exactly as
// passed (often already formatted: formatCount, formatCompact) and
// {placeholders} work as usual. Nested plural blocks are not supported.
//
// The count is the variable's number; a formatted string counts only when it
// is a plain integer, with or without digit-group separators ("1", "1,234",
// "1.234"); anything else ("1.2K", "1.5万", "4.5") selects `other`.
//
// Self-contained on purpose: the i18n check (plain Node) imports this file.

export type PluralBlock = {name: string; branches: Map<string, string>; start: number; end: number};

/** The plural blocks of a template, in order; throws on a malformed block. */
export function scanPlurals(template: string): PluralBlock[] {
  const blocks: PluralBlock[] = [];
  const head = /\{\s*(\w+)\s*,\s*plural\s*,/g;
  let m: RegExpExecArray | null;
  while ((m = head.exec(template))) {
    const start = m.index;
    let i = head.lastIndex;
    const branches = new Map<string, string>();
    for (;;) {
      while (i < template.length && /\s/.test(template[i])) i++;
      if (i >= template.length) throw new Error(`unclosed plural block '${m[1]}'`);
      if (template[i] === '}') {
        i++;
        break;
      }
      const sel = /^(=\d+|zero|one|two|few|many|other)\s*\{/.exec(template.slice(i));
      if (!sel) throw new Error(`plural block '${m[1]}': expected a selector at "${template.slice(i, i + 12)}"`);
      if (branches.has(sel[1])) throw new Error(`plural block '${m[1]}': duplicate '${sel[1]}'`);
      i += sel[0].length;
      let depth = 1;
      const from = i;
      while (i < template.length && depth > 0) {
        if (template[i] === '{') depth++;
        else if (template[i] === '}') depth--;
        i++;
      }
      if (depth > 0) throw new Error(`plural block '${m[1]}': unclosed '${sel[1]}' branch`);
      const text = template.slice(from, i - 1);
      if (/\{\s*\w+\s*,\s*plural\s*,/.test(text)) throw new Error(`plural block '${m[1]}': nested plural blocks are not supported`);
      branches.set(sel[1], text);
    }
    if (!branches.has('other')) throw new Error(`plural block '${m[1]}' has no 'other' branch`);
    blocks.push({name: m[1], branches, start, end: i});
    head.lastIndex = i;
  }
  return blocks;
}

/** The number a plural block selects on, or null when it is not a plain number. */
export function pluralCount(value: string | number | undefined): number | null {
  if (typeof value === 'number') return Number.isFinite(value) ? value : null;
  if (typeof value !== 'string') return null;
  const v = value.trim();
  if (/^-?\d+$/.test(v)) return Number(v);
  // Digit groups of three ("1,234", "1.234", "1 234"); "0.5" or "1.0" is not one.
  if (/^-?\d{1,3}(?:[,.'\s\u00a0\u202f]\d{3})+$/.test(v)) return Number(v.replace(/\D/g, '')) * (v.startsWith('-') ? -1 : 1);
  return null;
}

const rulesCache = new Map<string, Intl.PluralRules>();

function rules(lang: string): Intl.PluralRules {
  let r = rulesCache.get(lang);
  if (!r) {
    try {
      r = new Intl.PluralRules(lang);
    } catch {
      r = new Intl.PluralRules('en');
    }
    rulesCache.set(lang, r);
  }
  return r;
}

function pick(branches: Map<string, string>, count: number | null, lang: string): string {
  if (count !== null) {
    const exact = branches.get(`=${count}`);
    if (exact !== undefined) return exact;
    const cat = branches.get(rules(lang).select(count));
    if (cat !== undefined) return cat;
  }
  return branches.get('other') ?? '';
}

/** Replaces each plural block with its branch for `lang`; a malformed template is returned as is. */
export function expandPlurals(template: string, vars: Record<string, string | number> | undefined, lang: string): string {
  if (!template.includes('plural')) return template;
  let blocks: PluralBlock[];
  try {
    blocks = scanPlurals(template);
  } catch {
    return template;
  }
  if (blocks.length === 0) return template;
  let out = '';
  let at = 0;
  for (const b of blocks) {
    const value = vars?.[b.name];
    const branch = pick(b.branches, pluralCount(value), lang);
    out += template.slice(at, b.start) + (value === undefined ? branch : branch.replace(/#/g, String(value)));
    at = b.end;
  }
  return out + template.slice(at);
}
