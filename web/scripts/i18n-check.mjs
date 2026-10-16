#!/usr/bin/env node
// UI string completeness (PRD 8; tech plan 3.10): `npm run i18n:check`.
//
// The bundled zh-CN dictionary (app/lib/messages*.zh-CN.ts) is the source of
// every key. Bundled translations live in app/locales/{lang}.json (flat
// key → string maps, lib/locales.ts); reviewed ui_strings rows from the API
// override them at runtime. The check fails when:
//
//   1. a t('key') literal in app/ is not a zh-CN key;
//   2. a zh-CN key is never referenced from app/ (stale source key);
//   3. a locale misses a zh-CN key, or has a key zh-CN no longer has;
//   4. a locale value is empty, or its {placeholders} differ from zh-CN's;
//   5. a locale whose script has no Han characters (en, ko, vi, es, th)
//      still has Han characters in a value (left untranslated);
//   6. a required locale file is missing;
//   7. a plural block ({n, plural, one {...} other {...}}, app/lib/plural.ts)
//      is malformed, has no `other` branch, misses `one` in a language that
//      has it, uses a category the language does not have, or its variable
//      or branch placeholders are not zh-CN placeholders.
//
// With a plural block, placeholders compare as sets: the block's variable
// plus every {name} outside and inside its branches must equal zh-CN's.
//
// A key counts as referenced when a string literal anywhere in app/ equals
// it (t('a.b'), label maps typed MessageKey, ...), or when a template literal
// passed to t() or cast `as MessageKey` starts with its prefix
// (t(`performerReq.kind.${k}`)).
//
// Flags: --json prints the zh-CN source map (for translators) and exits.

import {readdirSync, readFileSync, statSync, existsSync} from 'node:fs';
import {join, relative, dirname} from 'node:path';
import {fileURLToPath} from 'node:url';
import ts from 'typescript';
import {scanPlurals} from '../app/lib/plural.ts';

const WEB = join(dirname(fileURLToPath(import.meta.url)), '..');
const APP = join(WEB, 'app');
const LIB = join(APP, 'lib');
const LOCALES = join(APP, 'locales');

// Languages with a bundled dictionary. zh-CN is the source; zh-TW is served
// from reviewed ui_strings only (falls back to zh-CN) until it gets a file.
const REQUIRED = ['en', 'ja', 'ko', 'vi', 'es', 'th'];
const NO_HAN = new Set(['en', 'ko', 'vi', 'es', 'th']);

const errors = [];
const fail = msg => errors.push(msg);

const isSource = f => /^messages(\.[a-z]+)?\.zh-CN\.ts$/.test(f);

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) {
      if (name !== 'locales') walk(p, out);
    } else if (/\.(ts|tsx)$/.test(name) && !name.endsWith('.d.ts')) out.push(p);
  }
  return out;
}

const parse = file => ts.createSourceFile(file, readFileSync(file, 'utf8'), ts.ScriptTarget.Latest, true, file.endsWith('x') ? ts.ScriptKind.TSX : ts.ScriptKind.TS);
const isStr = n => n && (ts.isStringLiteral(n) || ts.isNoSubstitutionTemplateLiteral(n));
const where = (sf, node) => `${relative(WEB, sf.fileName)}:${sf.getLineAndCharacterOfPosition(node.getStart(sf)).line + 1}`;

// ---- zh-CN source ----

const source = new Map(); // key -> value
const sourceAt = new Map();
for (const name of readdirSync(LIB).filter(isSource).sort()) {
  const sf = parse(join(LIB, name));
  const visit = node => {
    if (ts.isPropertyAssignment(node) && isStr(node.name)) {
      const key = node.name.text;
      if (!isStr(node.initializer)) fail(`${where(sf, node)}: zh-CN value of '${key}' is not a plain string`);
      else if (source.has(key)) fail(`${where(sf, node)}: duplicate zh-CN key '${key}' (also ${sourceAt.get(key)})`);
      else {
        source.set(key, node.initializer.text);
        sourceAt.set(key, where(sf, node));
      }
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

if (process.argv.includes('--json')) {
  process.stdout.write(JSON.stringify(Object.fromEntries(source), null, 2) + '\n');
  process.exit(0);
}

// ---- References from app/ ----

const used = new Set();
const prefixes = new Set();

function templatePrefix(node) {
  return ts.isTemplateExpression(node) && /^[a-zA-Z][\w.]*\.$/.test(node.head.text) ? node.head.text : null;
}

/** t(<expr>): literals must be keys; templates add a prefix; ?: checks both sides. */
function keyArg(sf, node) {
  if (!node) return;
  if (ts.isParenthesizedExpression(node) || ts.isAsExpression(node)) return keyArg(sf, node.expression);
  if (ts.isConditionalExpression(node)) {
    keyArg(sf, node.whenTrue);
    keyArg(sf, node.whenFalse);
  } else if (ts.isBinaryExpression(node) && node.operatorToken.kind === ts.SyntaxKind.QuestionQuestionToken) {
    keyArg(sf, node.right);
  } else if (isStr(node)) {
    if (!source.has(node.text)) fail(`${where(sf, node)}: t('${node.text}') is not a zh-CN key`);
  } else {
    const p = templatePrefix(node);
    if (p) prefixes.add(p);
  }
}

for (const file of walk(APP)) {
  if (isSource(file.slice(LIB.length + 1)) && dirname(file) === LIB) continue;
  const sf = parse(file);
  const visit = node => {
    if (isStr(node) && source.has(node.text)) used.add(node.text);
    if (ts.isCallExpression(node)) {
      const callee = node.expression;
      const name = ts.isIdentifier(callee) ? callee.text : ts.isPropertyAccessExpression(callee) ? callee.name.text : '';
      if (name === 't') keyArg(sf, node.arguments[0]);
    }
    if (ts.isAsExpression(node) && ts.isTypeReferenceNode(node.type) && node.type.typeName.getText(sf) === 'MessageKey') {
      const p = templatePrefix(node.expression);
      if (p) prefixes.add(p);
    }
    ts.forEachChild(node, visit);
  };
  visit(sf);
}

for (const p of prefixes) {
  let any = false;
  for (const k of source.keys()) if (k.startsWith(p)) (used.add(k), (any = true));
  if (!any) fail(`dynamic key prefix '${p}' matches no zh-CN key`);
}
for (const k of source.keys()) if (!used.has(k)) fail(`${sourceAt.get(k)}: zh-CN key '${k}' is not used in app/ (remove it)`);

// ---- Locales ----

const placeholderList = s => [...s.matchAll(/\{(\w+)\}/g)].map(m => m[1]);
const placeholders = s => placeholderList(s).sort().join(',');

/** Plural blocks of `s`, or null (reported) when malformed. */
function pluralBlocks(where, key, s) {
  try {
    return scanPlurals(s);
  } catch (e) {
    fail(`${where}: '${key}' ${e.message}`);
    return null;
  }
}

/** Placeholder names of a value with plural blocks: variables, outside and branch {names}. */
function pluralNames(s, blocks) {
  const names = new Set();
  let at = 0;
  for (const b of blocks) {
    placeholderList(s.slice(at, b.start)).forEach(n => names.add(n));
    names.add(b.name);
    for (const text of b.branches.values()) placeholderList(text).forEach(n => names.add(n));
    at = b.end;
  }
  placeholderList(s.slice(at)).forEach(n => names.add(n));
  return [...names].sort().join(',');
}

const categories = lang => new Set(new Intl.PluralRules(lang).resolvedOptions().pluralCategories);

/** Checks a value's plural blocks for `lang`; returns its placeholder signature. */
function checkPlurals(rel, key, value, lang) {
  const blocks = pluralBlocks(rel, key, value);
  if (!blocks) return null;
  if (!blocks.length) return {sig: placeholders(value), plural: false};
  const cats = categories(lang);
  for (const b of blocks) {
    for (const sel of b.branches.keys()) {
      if (!sel.startsWith('=') && !cats.has(sel)) fail(`${rel}: '${key}' plural '${sel}' is not a ${lang} plural category (${[...cats].join(', ')})`);
    }
    if (cats.has('one') && !b.branches.has('one')) fail(`${rel}: '${key}' plural block '${b.name}' needs a 'one' branch in ${lang}`);
  }
  return {sig: pluralNames(value, blocks), plural: true};
}

const sourceSig = new Map();
for (const [k, v] of source) {
  const r = checkPlurals(sourceAt.get(k), k, v, 'zh-CN');
  if (r) sourceSig.set(k, r);
}
const nameSet = sig => [...new Set(sig.split(',').filter(Boolean))].sort().join(',');
const HAN = /\p{Script=Han}/u;

const files = existsSync(LOCALES) ? readdirSync(LOCALES).filter(f => f.endsWith('.json')) : [];
for (const lang of REQUIRED) if (!files.includes(`${lang}.json`)) fail(`app/locales/${lang}.json is missing`);

const counts = {'zh-CN': source.size};
for (const f of files.sort()) {
  const lang = f.slice(0, -5);
  const rel = `app/locales/${f}`;
  let dict;
  try {
    dict = JSON.parse(readFileSync(join(LOCALES, f), 'utf8'));
  } catch (e) {
    fail(`${rel}: invalid JSON (${e.message})`);
    continue;
  }
  counts[lang] = Object.keys(dict).length;
  const missing = [...source.keys()].filter(k => !(k in dict));
  if (missing.length) fail(`${rel}: ${missing.length} missing key(s): ${missing.slice(0, 20).join(', ')}${missing.length > 20 ? ', ...' : ''}`);
  for (const [k, v] of Object.entries(dict)) {
    if (!source.has(k)) {
      fail(`${rel}: '${k}' is not a zh-CN key (stale)`);
      continue;
    }
    if (typeof v !== 'string' || !v.trim()) {
      fail(`${rel}: '${k}' is empty`);
      continue;
    }
    const r = checkPlurals(rel, k, v, lang);
    const src = sourceSig.get(k);
    if (!r || !src) continue;
    // Sets once either side has a plural block (a branch may omit or repeat a name).
    const plural = r.plural || src.plural;
    const want = plural ? nameSet(src.sig) : src.sig;
    const got = plural ? nameSet(r.sig) : r.sig;
    if (want !== got) fail(`${rel}: '${k}' placeholders {${got}} differ from zh-CN {${want}}`);
    if (NO_HAN.has(lang) && HAN.test(v)) fail(`${rel}: '${k}' still contains Chinese text: ${JSON.stringify(v)}`);
  }
}

if (errors.length) {
  for (const e of errors) console.error(`i18n: ${e}`);
  console.error(`i18n: ${errors.length} problem(s)`);
  process.exit(1);
}
console.log(`i18n: ok, ${Object.entries(counts).map(([l, n]) => `${l} ${n}`).join(', ')} keys; ${used.size} referenced`);
