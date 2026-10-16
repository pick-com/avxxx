// Site-entry 18+ gate (DESIGN_ALIGNMENT #8, PRD 15; internal/api/agegate.go).
//
// The site is adults-only. The SSR server reads the signed `ag` cookie
// (ageGate.server.ts) and the root route renders the gate interstitial or the
// restricted page instead of the app (root.tsx) until the visitor has an ok
// verdict. The answer goes to POST /api/age-gate, which sets the cookie; the
// page then reloads so every loader runs again with full data.
//
// Not gated: legal documents, help and copyright (a refused visitor can still
// read them) and the back office.

import {api, ApiError} from './api';
import {stripLang} from './langs';

/** none = not answered yet (or expired); denied = under 18, locked 24 h. */
export type GateState = 'ok' | 'denied' | 'none';

export const ADULT_AGE = 18;

const EXEMPT = /^\/(legal(\/|$)|help$|copyright$|admin(\/|$))/;

/** Pages shown without the gate (unprefixed or language-prefixed path). */
export function isGateExempt(pathname: string): boolean {
  return EXEMPT.test(stripLang(pathname) || '/');
}

/**
 * Whole years from a birth year and month. The day is unknown, so the birth
 * month itself counts as not yet reached (conservative; same rule as the API).
 */
export function ageFromYearMonth(year: number, month: number, now = new Date()): number {
  let age = now.getFullYear() - year;
  if (now.getMonth() + 1 <= month) age -= 1;
  return age;
}

/** 403 from an account 18+ check: under 18, or the gate cookie is denied (the restricted page takes over on reload). */
export const isAgeRestricted = (cause: unknown) => cause instanceof ApiError && cause.status === 403 && cause.detail === 'age restricted';

export type GateAnswer ={status: 'ok' | 'denied'; locked_until?: string};

/** POST /api/age-gate. A 403 means a denial is still locked. */
export async function answerAgeGate(confirmed = true): Promise<GateAnswer> {
  try {
    return await api<GateAnswer>('age-gate', {method: 'POST', body: {confirmed}});
  } catch (cause) {
    if (cause instanceof ApiError && cause.status === 403) return {status: 'denied'};
    throw cause;
  }
}
