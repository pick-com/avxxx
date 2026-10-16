// 版权凭证 client (internal/api/admin_release_rights.go): private proof that
// a release may be listed. Back office only.

import {api, query} from './api';

export type RightsProofKind = 'license' | 'contract' | 'authorization' | 'invoice' | 'other';

export const RIGHTS_KIND_LABELS: Record<RightsProofKind, string> = {
  license: '授权许可',
  contract: '合同',
  authorization: '授权书',
  invoice: '发票 / 付款凭证',
  other: '其他',
};

export type RightsProof = {
  id: number;
  release_id: number;
  kind: RightsProofKind;
  reference: string;
  url: string;
  note: string;
  valid_until: string | null;
  created_by: string;
  created_at: string;
  removed_at: string | null;
  removed_by: string | null;
};

export type RightsProofInput = {kind: RightsProofKind; reference: string; url: string; note: string; valid_until: string};

export const getRightsProofs = (releaseId: number, removed: boolean, signal?: AbortSignal) =>
  api<{items: RightsProof[] | null}>(`admin/releases/${releaseId}/rights-proofs${query({removed: removed ? 1 : null})}`, {signal}).then(d => d.items ?? []);
export const addRightsProof = (releaseId: number, body: RightsProofInput) =>
  api<RightsProof>(`admin/releases/${releaseId}/rights-proofs`, {method: 'POST', body});
export const removeRightsProof = (releaseId: number, proofId: number, reason: string) =>
  api<RightsProof>(`admin/releases/${releaseId}/rights-proofs/${proofId}${query({reason})}`, {method: 'DELETE'});
