// 「完成设置」 nudge after a sign-up from the sign-in sheet (components/AuthPrompt.tsx).
//
// /login sends a new account to /welcome (Onboarding) before `next`; the
// sheet keeps the guest on the page instead, so the action they started can
// finish. A small, dismissible prompt then links to /welcome?next=<page>,
// only while the account has not finished onboarding (users.onboarded_at,
// GET /api/auth/me `onboarded`).

/**
 * What to do with a sign-up waiting for its profile: 'show' once the profile
 * says onboarding is not done, 'drop' when it is done (or the account was not
 * created here), 'wait' while the full profile (GET /api/auth/me, loaded
 * right after the sign-in) has not arrived. The sign-up response itself has
 * no `onboarded`, and neither does an older backend, which therefore never
 * shows it.
 */
export function onboardingNudge(created: boolean, me: {onboarded?: boolean} | null): 'show' | 'drop' | 'wait' {
  if (!created) return 'drop';
  if (!me || typeof me.onboarded !== 'boolean') return 'wait';
  return me.onboarded ? 'drop' : 'show';
}

/** /welcome returning to `here` (an unprefixed path with its query). */
export function welcomeHref(here: string): string {
  const next = here.startsWith('/') && !here.startsWith('//') ? here : '/';
  return `/welcome?next=${encodeURIComponent(next)}`;
}
