// Values inside the backend's zh-CN 推荐理由 text (internal/recs ReasonFor)
// that a localized reason keeps: the chart rank of a trending reason and the
// shared tag of a similar_tags one. Pure (node --test).

/** "本周热门 · 第 3 名" → 3; no rank → null. */
export function trendingRank(text: string | undefined): number | null {
  const m = /第\s*(\d+)\s*名/.exec(text ?? '');
  return m ? Number(m[1]) : null;
}

/** "相似标签 · 4K" → "4K"; "内容相似" → "". */
export function sharedTag(text: string | undefined): string {
  const m = /^相似标签\s*·\s*(.+)$/.exec((text ?? '').trim());
  return m ? m[1].trim() : '';
}
