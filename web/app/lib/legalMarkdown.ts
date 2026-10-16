// The small Markdown subset legal_documents.body uses (migration 041), parsed
// to plain data that components/LegalMarkdown.tsx renders as React elements
// (never innerHTML), so document text cannot inject markup: `<script>` and
// any other HTML stay literal text.
//
//   # Heading / ## Heading {#anchor}   section heading (h2, numbered)
//   ### Sub (and ####–######)          sub heading (h3)
//   - item / * item                     bullet list
//   1. item                             numbered list
//   **bold** / __bold__                 strong
//   *em* / _em_                         emphasis (`_` only around whole words)
//   [text](url)                         link: http(s)://, mailto: or relative
//                                       (/path, ./x, ../x, #anchor, page);
//                                       any other target renders as text
//
// Paragraphs are separated by blank lines; consecutive lines join with a
// space. A legal page already has the document title as its h1, so `#` is a
// section heading like `##`.
//
// Self-contained (no React) so scripts/legal-markdown.test.mjs can run it.

export type Inline =
  | {kind: 'text'; text: string}
  | {kind: 'strong' | 'em'; children: Inline[]}
  | {kind: 'link'; href: string; external: boolean; children: Inline[]};

export type LegalBlock =
  | {kind: 'h2' | 'h3'; id: string; text: string}
  | {kind: 'p'; text: string}
  | {kind: 'ul' | 'ol'; items: string[]};

export type LegalHeading = {id: string; title: string};

const HEADING = /^(#{1,6})\s+(.*?)(?:\s+\{#([A-Za-z0-9_-]+)\})?\s*#*\s*$/;
const BULLET = /^\s*[-*+]\s+/;
const NUMBERED = /^\s*\d{1,9}[.)]\s+/;

const slug = (s: string, i: number) => {
  const v = plainText(parseInline(s))
    .toLowerCase()
    .replace(/[^\p{L}\p{N}]+/gu, '-')
    .replace(/^-+|-+$/g, '');
  return v || `section-${i + 1}`;
};

export function parseLegalMarkdown(md: string): LegalBlock[] {
  const blocks: LegalBlock[] = [];
  const ids = new Set<string>();
  let para: string[] = [];
  let list: {kind: 'ul' | 'ol'; items: string[]} | null = null;
  const flush = () => {
    if (para.length) blocks.push({kind: 'p', text: para.join(' ')});
    if (list) blocks.push(list);
    para = [];
    list = null;
  };
  const uniqueId = (id: string) => {
    let v = id;
    for (let n = 2; ids.has(v); n++) v = `${id}-${n}`;
    ids.add(v);
    return v;
  };
  for (const raw of md.replace(/\r\n?/g, '\n').split('\n')) {
    const line = raw.trimEnd();
    const h = HEADING.exec(line);
    const listKind = BULLET.test(line) ? 'ul' : NUMBERED.test(line) ? 'ol' : null;
    if (h) {
      flush();
      blocks.push({kind: h[1].length <= 2 ? 'h2' : 'h3', text: h[2], id: uniqueId(h[3] || slug(h[2], blocks.length))});
    } else if (listKind) {
      if (para.length || (list && list.kind !== listKind)) flush();
      list ??= {kind: listKind, items: []};
      list.items.push(line.replace(listKind === 'ul' ? BULLET : NUMBERED, ''));
    } else if (!line.trim()) {
      flush();
    } else if (list && /^\s{2,}\S/.test(raw) && list.items.length) {
      // An indented line continues the previous list item.
      list.items[list.items.length - 1] += ' ' + line.trim();
    } else {
      if (list) flush();
      para.push(line.trim());
    }
  }
  flush();
  return blocks;
}

export const legalHeadings = (blocks: LegalBlock[]): LegalHeading[] =>
  blocks.flatMap(b => (b.kind === 'h2' ? [{id: b.id, title: plainText(parseInline(b.text))}] : []));

/**
 * The link target when it is allowed (http, https, mailto, or relative),
 * otherwise null. Protocol-relative `//host` and every other scheme
 * (javascript:, data:, vbscript:, …) are refused.
 */
export function safeHref(raw: string): string | null {
  const href = raw.trim();
  if (!href || /[\s<>"'`\\]/.test(href) || /[\u0000-\u001f\u007f]/.test(href)) return null;
  if (/^https?:\/\/[^/]/i.test(href)) return href;
  if (/^mailto:[^/]/i.test(href)) return href;
  if (href.startsWith('//')) return null;
  // Relative: no scheme before the first /, ?, or #.
  if (/^[a-z][a-z0-9+.-]*:/i.test(href)) return null;
  return href;
}

export const isExternalHref = (href: string) => /^https?:/i.test(href);

// Earliest of: **bold** __bold__ *em* _em_ [text](url). Underscore emphasis
// needs a non-word character (or the edge) outside it, so snake_case stays.
const INLINE =
  /\*\*(?=\S)([\s\S]+?)\*\*|(?<![\p{L}\p{N}_])__(?=\S)([\s\S]+?)__(?![\p{L}\p{N}_])|\*(?=[^\s*])([\s\S]+?)\*|(?<![\p{L}\p{N}_])_(?=[^\s_])([\s\S]+?)_(?![\p{L}\p{N}_])|\[([^\]]+)\]\(([^()\s]*(?:\([^()\s]*\)[^()\s]*)*)\)/gu;

export function parseInline(text: string, depth = 0): Inline[] {
  const out: Inline[] = [];
  let last = 0;
  const push = (s: string) => {
    if (!s) return;
    const prev = out[out.length - 1];
    if (prev?.kind === 'text') prev.text += s;
    else out.push({kind: 'text', text: s});
  };
  if (depth > 4) {
    push(text);
    return out;
  }
  for (const m of text.matchAll(INLINE)) {
    const at = m.index ?? 0;
    push(text.slice(last, at));
    last = at + m[0].length;
    const strong = m[1] ?? m[2];
    const em = m[3] ?? m[4];
    if (strong !== undefined) out.push({kind: 'strong', children: parseInline(strong, depth + 1)});
    else if (em !== undefined) out.push({kind: 'em', children: parseInline(em, depth + 1)});
    else {
      const href = safeHref(m[6]);
      const children = parseInline(m[5], depth + 1);
      if (href) out.push({kind: 'link', href, external: isExternalHref(href), children});
      else children.forEach(c => (c.kind === 'text' ? push(c.text) : out.push(c)));
    }
  }
  push(text.slice(last));
  return out;
}

/** The visible text of inline nodes (headings in the table of contents). */
export function plainText(nodes: Inline[]): string {
  return nodes.map(n => (n.kind === 'text' ? n.text : plainText(n.children))).join('');
}

const ESC: Record<string, string> = {'&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'};
const esc = (s: string) => s.replace(/[&<>"']/g, c => ESC[c]);

function inlineHtml(nodes: Inline[]): string {
  return nodes
    .map(n => {
      if (n.kind === 'text') return esc(n.text);
      if (n.kind !== 'link') return `<${n.kind}>${inlineHtml(n.children)}</${n.kind}>`;
      const ext = n.external ? ' target="_blank" rel="noopener noreferrer"' : '';
      return `<a href="${esc(n.href)}"${ext}>${inlineHtml(n.children)}</a>`;
    })
    .join('');
}

/**
 * Escaped HTML for the blocks: the same output the React component renders,
 * for tests and non-React consumers. Every piece of document text is escaped.
 */
export function legalMarkdownToHtml(md: string): string {
  return parseLegalMarkdown(md)
    .map(b => {
      switch (b.kind) {
        case 'h2':
        case 'h3':
          return `<${b.kind} id="${esc(b.id)}">${inlineHtml(parseInline(b.text))}</${b.kind}>`;
        case 'ul':
        case 'ol':
          return `<${b.kind}>${b.items.map(i => `<li>${inlineHtml(parseInline(i))}</li>`).join('')}</${b.kind}>`;
        default:
          return `<p>${inlineHtml(parseInline(b.text))}</p>`;
      }
    })
    .join('\n');
}
