import {api} from './api';
import type {MessageKey} from './messages.zh-CN';
import {useAsync} from './useAsync';

// Public source evidence (internal/evidence; GET /api/releases/{slug}/evidence,
// GET /api/performers/{slug}/evidence) and source credits
// (GET /api/attributions, sources.attribution_html).
//
// Evidence never carries raw source payloads, override reasons or actors, and
// hidden fields (a hidden birthday, PRD 15) are left out by the API.

export type EvidenceKind = 'release' | 'performer';

export type EvidenceSource = {
  name: string;
  source_kind: string;
  tier: number;
  url?: string;
  first_seen_at: string;
  last_seen_at: string;
};

export type EvidenceSourceValue = {
  source: string;
  source_kind: string;
  tier: number;
  value: unknown;
  updated_at: string;
  last_seen_at: string;
  matches: boolean;
};

export type FieldEvidence = {
  field: string;
  value: unknown;
  overridden: boolean;
  provided_by: {source: string; source_kind: string; tier: number} | null;
  updated_at: string | null;
  sources: EvidenceSourceValue[] | null;
};

export type Evidence = {
  entity_type: EvidenceKind;
  slug: string;
  updated_at: string;
  sources: EvidenceSource[] | null;
  fields: FieldEvidence[] | null;
};

export const getEvidence = (kind: EvidenceKind, slug: string, signal?: AbortSignal) =>
  api<Evidence>(`${kind === 'release' ? 'releases' : 'performers'}/${encodeURIComponent(slug)}/evidence`, {signal});

/** FINAL_DATA_DESIGN 2.4 tiers (internal/evidence.Tier). */
export function tierLabel(tier: number): MessageKey {
  switch (tier) {
    case 0:
      return 'evidence.tier0';
    case 1:
      return 'evidence.tier1';
    case 2:
      return 'evidence.tier2';
    case 3:
      return 'evidence.tier3';
    case 4:
      return 'evidence.tier4';
    case 5:
      return 'evidence.tier5';
  }
  return 'evidence.tierOther';
}

const FIELD_KEYS = new Set([
  'canonical_title', 'title_ja', 'dvd_code', 'content_id', 'release_date', 'runtime_minutes', 'maker_name', 'label_name',
  'series_name', 'director_name', 'synopsis', 'canonical_name', 'canonical_name_ja', 'birth_date', 'birth_place',
  'debut_date', 'blood_type', 'height_cm', 'bust_cm', 'waist_cm', 'hip_cm', 'cup_size', 'shoe_size', 'biography',
]);

/** Label key of an evidence field; unknown fields show their name. */
export const fieldLabel = (field: string): MessageKey | null => (FIELD_KEYS.has(field) ? (`evidence.f.${field}` as MessageKey) : null);

// ---- Source credits ----

/** One source's required credit; html is exactly as stored and rendered verbatim. */
export type Attribution = {source: string; html: string};

/** What the credits cover: one entity's page, or (kind null) any data on the site. */
export type CreditScope = {kind: 'release' | 'performer' | 'series'; slug: string} | {kind: null};

export const attributionsPath = (scope: CreditScope) =>
  scope.kind ? `attributions?${scope.kind}=${encodeURIComponent(scope.slug)}` : 'attributions';

export const getAttributions = (scope: CreditScope, signal?: AbortSignal) =>
  api<{items: Attribution[] | null}>(attributionsPath(scope), {signal}).then(d => d.items ?? []);

/**
 * Credits for a page. `initial` is what the route loader fetched (server
 * rendering); client-side navigations fetch after mount. A backend without
 * the route, or any failure, shows no credits rather than an error.
 */
export function useCredits(scope: CreditScope, initial?: Attribution[]) {
  const key = scope.kind ? `${scope.kind}:${scope.slug}` : '';
  const state = useAsync(signal => getAttributions(scope, signal).catch(() => [] as Attribution[]), [key], true, initial);
  return state.data ?? [];
}
