import {useId, useState} from 'react';
import {CONTENT_REPORT_REASONS, postErrorMessage, type ReportReason} from '../lib/community';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';

const REASON_KEYS: Record<ReportReason, MessageKey> = {
  spoiler: 'report.spoiler',
  spam: 'report.spam',
  abuse: 'report.abuse',
  illegal: 'report.illegal',
  underage: 'report.underage',
  privacy: 'report.privacy',
  other: 'report.other',
};

/** Inline reason picker for 举报 (danmaku, reviews): one reason, then 提交. */
export function ReportPicker({onSubmit, onCancel}: {onSubmit: (reason: ReportReason) => Promise<void>; onCancel: () => void}) {
  const t = useT();
  const id = useId();
  const [reason, setReason] = useState<ReportReason | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');

  const submit = async () => {
    if (!reason) return;
    setBusy(true);
    setError('');
    try {
      await onSubmit(reason);
    } catch (cause) {
      setError(postErrorMessage(cause));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="report-picker">
      <span id={id} className="report-title">
        {t('report.title')}
      </span>
      <div className="chip-row chip-row-wrap" role="radiogroup" aria-labelledby={id}>
        {CONTENT_REPORT_REASONS.map(r => (
          <button key={r} type="button" role="radio" aria-checked={reason === r} className={reason === r ? 'chip chip-on' : 'chip'} onClick={() => setReason(r)}>
            {t(REASON_KEYS[r])}
          </button>
        ))}
      </div>
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
      <div className="report-actions">
        <button type="button" className="btn btn-secondary btn-sm" onClick={onCancel} disabled={busy}>
          {t('report.cancel')}
        </button>
        <button type="button" className="btn btn-primary btn-sm" onClick={() => void submit()} disabled={!reason || busy}>
          {busy ? t('report.submitting') : t('report.submit')}
        </button>
      </div>
    </div>
  );
}
