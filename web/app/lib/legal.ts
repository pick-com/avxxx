// Legal documents (LegalPage, PRD 15) are versioned rows of legal_documents
// (migration 041, GET /api/legal/{kind}); the texts that used to live here
// are version 1 of each kind. This module only lists the document pages for
// the sitemap and keeps the bundled terms-version fallback.

import {LEGAL_KINDS} from './compliance';

/**
 * Version of the 用户协议 users accept (terms_acceptances). Bump it when the
 * terms change: signed-in users are asked to accept again. Superseded by
 * GET /api/legal/terms/current-version where that backend exists
 * (lib/account.ts currentTermsVersion).
 */
export const TERMS_VERSION = '2026-10-draft';

export const LEGAL_DOCS: {slug: string}[] = LEGAL_KINDS.map(slug => ({slug}));
