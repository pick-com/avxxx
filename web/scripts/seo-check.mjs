#!/usr/bin/env node
// 收录健康分 inputs (PRD 11 / 13): `npm run seo:check`.
//
// 1. Sitemap coverage: reads /sitemap.xml from the running web server,
//    counts the zh-CN URLs per type (releases, performers, tags) and compares
//    them with GET /api/sitemap/counts (what should be listed). Every type
//    must also have a file in each of the 8 languages.
// 2. SSR pass rate: fetches SEO_SAMPLES random URLs per type from the
//    sitemaps (random language) without JavaScript and checks each page:
//    status 200, non-empty <h1>, self-referencing canonical, hreflang
//    alternates for all languages plus x-default, <html lang>, no noindex.
// 3. Report: score = round(100 × (ssr_pass_rate × 0.6 + sitemap_coverage × 0.4)).
//    Printed as JSON; written to SEO_REPORT_FILE when set; POSTed to
//    {API_URL}/api/admin/seo/report when ADMIN_TOKEN is set, where the admin
//    dashboard shows it as 收录健康分 (nightly_runs job "seo.check").
//
// Environment:
//   WEB_URL          web server to check (default http://127.0.0.1:3000)
//   API_URL          Go API (default $API_INTERNAL_URL or http://127.0.0.1:8080)
//   ADMIN_TOKEN      bearer token for POST /api/admin/seo/report (optional)
//   SEO_SAMPLES      pages per type (default 5)
//   SEO_REPORT_FILE  also write the JSON report here (optional)
//   SEO_MIN_SCORE    exit 1 when the score is below this (optional)

import {writeFileSync} from 'node:fs';

const WEB = (process.env.WEB_URL || 'http://127.0.0.1:3000').replace(/\/+$/, '');
const API = (process.env.API_URL || process.env.API_INTERNAL_URL || 'http://127.0.0.1:8080').replace(/\/+$/, '');
const SAMPLES = Math.max(1, Number(process.env.SEO_SAMPLES) || 5);
const LANGS = ['zh-CN', 'zh-TW', 'en', 'ja', 'ko', 'vi', 'es', 'th'];
const TYPES = ['pages', 'releases', 'performers', 'tags'];
const COUNTED = ['releases', 'performers', 'tags'];
const UA = 'avxxx-seo-check/1.0';

const started = Date.now();

async function get(url, accept = 'text/html') {
  const r = await fetch(url, {redirect: 'manual', headers: {'User-Agent': UA, Accept: accept}, signal: AbortSignal.timeout(15000)});
  return {status: r.status, text: await r.text(), headers: r.headers};
}

/** Fetch a public URL from the sitemaps through WEB_URL (the sitemaps carry PUBLIC_BASE_URL). */
const viaWeb = url => {
  const u = new URL(url);
  return WEB + u.pathname + u.search;
};

const locs = xml => [...xml.matchAll(/<loc>([^<]+)<\/loc>/g)].map(m => m[1].replace(/&amp;/g, '&'));
const decode = s => s.replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#x27;/g, "'").replace(/&lt;/g, '<').replace(/&gt;/g, '>');

function checkPage(html, expectedUrl, lang) {
  const reasons = [];
  const h1 = /<h1[^>]*>([\s\S]*?)<\/h1>/i.exec(html);
  if (!h1 || !decode(h1[1].replace(/<[^>]+>/g, '')).trim()) reasons.push('h1');
  const canonical = /<link rel="canonical" href="([^"]+)"/.exec(html)?.[1];
  if (!canonical) reasons.push('canonical');
  else if (decode(canonical) !== expectedUrl) reasons.push('canonical_mismatch');
  const hreflangs = new Set([...html.matchAll(/<link rel="alternate" hrefLang="([^"]+)"/g)].map(m => m[1]));
  if (![...LANGS, 'x-default'].every(l => hreflangs.has(l))) reasons.push('hreflang');
  if (/<html lang="([^"]+)"/.exec(html)?.[1] !== lang) reasons.push('html_lang');
  if (/<meta name="robots" content="[^"]*noindex/.test(html)) reasons.push('noindex');
  return reasons;
}

function sample(list, n) {
  const copy = [...list];
  for (let i = copy.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [copy[i], copy[j]] = [copy[j], copy[i]];
  }
  return copy.slice(0, n);
}

const report = {base_url: WEB, checked_at: new Date().toISOString(), samples_per_type: SAMPLES};

// ---- Sitemaps ----
const index = await get(`${WEB}/sitemap.xml`, 'application/xml');
if (index.status !== 200) {
  console.error(`sitemap.xml answered ${index.status}`);
  process.exit(2);
}
const files = locs(index.text);
const byType = Object.fromEntries(TYPES.map(t => [t, {files: {}, urls: {}}]));
for (const file of files) {
  const m = /\/sitemaps\/([a-z]+)-([A-Za-z-]+)-(\d+)\.xml$/.exec(new URL(file).pathname);
  if (!m || !byType[m[1]]) continue;
  const [, type, lang] = m;
  (byType[type].files[lang] ??= []).push(file);
}
const missingLangs = [];
for (const type of TYPES) {
  const langsWithFiles = Object.keys(byType[type].files);
  if (langsWithFiles.length && langsWithFiles.length !== LANGS.length) missingLangs.push(`${type}:${LANGS.filter(l => !langsWithFiles.includes(l)).join(',')}`);
  // zh-CN files are counted; one other language is read for sampling too.
  const other = sample(langsWithFiles.filter(l => l !== 'zh-CN'), 1);
  for (const lang of langsWithFiles.filter(l => l === 'zh-CN' || other.includes(l))) {
    for (const file of byType[type].files[lang]) {
      const r = await get(viaWeb(file), 'application/xml');
      if (r.status !== 200) throw new Error(`${file} answered ${r.status}`);
      (byType[type].urls[lang] ??= []).push(...locs(r.text));
    }
  }
}

let expected = null;
try {
  const r = await fetch(`${API}/api/sitemap/counts`, {signal: AbortSignal.timeout(15000)});
  if (r.ok) expected = await r.json();
} catch {
  /* reported below */
}
const sitemap = {};
let listedSum = 0;
let expectedSum = 0;
for (const type of COUNTED) {
  const listed = byType[type].urls['zh-CN']?.length ?? 0;
  const want = expected?.[type];
  sitemap[type] = {listed, expected: typeof want === 'number' ? want : null, files_per_lang: byType[type].files['zh-CN']?.length ?? 0};
  if (typeof want === 'number') {
    listedSum += Math.min(listed, want);
    expectedSum += want;
  }
}
report.sitemap = sitemap;
report.sitemap_missing_langs = missingLangs;
report.counts_source = expected ? `${API}/api/sitemap/counts` : 'unavailable';
let coverage = expectedSum > 0 ? listedSum / expectedSum : expected ? 1 : 0;
if (missingLangs.length) coverage *= 1 - missingLangs.length / (TYPES.length * LANGS.length);
report.sitemap_coverage = Math.round(coverage * 10000) / 10000;

// ---- SSR samples ----
const results = [];
for (const type of TYPES) {
  const pool = Object.entries(byType[type].urls).flatMap(([lang, urls]) => urls.map(url => ({url, lang})));
  for (const {url, lang} of sample(pool, SAMPLES)) {
    try {
      const r = await get(viaWeb(url));
      const reasons = r.status === 200 ? checkPage(r.text, url, lang) : [`status_${r.status}`];
      results.push({type, url, lang, ok: reasons.length === 0, reasons});
    } catch (e) {
      results.push({type, url, lang, ok: false, reasons: [`fetch: ${e instanceof Error ? e.message : String(e)}`]});
    }
  }
}
const passed = results.filter(r => r.ok).length;
report.checked = results.length;
report.failed = results.length - passed;
report.ssr_pass_rate = results.length ? Math.round((passed / results.length) * 10000) / 10000 : 0;
report.by_type = Object.fromEntries(
  TYPES.map(t => {
    const rs = results.filter(r => r.type === t);
    return [t, {checked: rs.length, passed: rs.filter(r => r.ok).length}];
  }),
);
report.failures = results.filter(r => !r.ok).slice(0, 20).map(({url, reasons}) => ({url, reasons}));
report.score = Math.round(100 * (report.ssr_pass_rate * 0.6 + report.sitemap_coverage * 0.4));
report.duration_ms = Date.now() - started;

console.log(JSON.stringify(report, null, 2));
if (process.env.SEO_REPORT_FILE) writeFileSync(process.env.SEO_REPORT_FILE, JSON.stringify(report, null, 2) + '\n');

if (process.env.ADMIN_TOKEN) {
  const r = await fetch(`${API}/api/admin/seo/report`, {
    method: 'POST',
    headers: {'Content-Type': 'application/json', Authorization: `Bearer ${process.env.ADMIN_TOKEN}`, 'X-Admin-User': 'seo-check'},
    body: JSON.stringify(report),
    signal: AbortSignal.timeout(15000),
  });
  if (!r.ok) {
    console.error(`POST /api/admin/seo/report answered ${r.status}: ${(await r.text()).slice(0, 200)}`);
    process.exit(2);
  }
  console.error(`recorded: score ${report.score} (job seo.check)`);
}

const min = Number(process.env.SEO_MIN_SCORE);
if (Number.isFinite(min) && min > 0 && report.score < min) {
  console.error(`score ${report.score} is below SEO_MIN_SCORE ${min}`);
  process.exit(1);
}
