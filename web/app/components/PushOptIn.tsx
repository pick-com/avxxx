import {useT} from '../lib/i18n';
import type {PushState} from '../lib/push';
import {Switch} from './Switch';

/** One line about this browser's push state (settings, onboarding). */
export function pushStatusText(push: PushState, t: ReturnType<typeof useT>): string {
  if (push.availability === 'unsupported') return t('push.unsupported');
  if (push.permission === 'denied' || push.error === 'denied') return t('push.denied');
  if (push.error === 'failed') return t('push.failed');
  if (push.subscribed) return t('push.deviceOn');
  return t('push.deviceOff');
}

/**
 * 本设备推送 row for Settings › 通知: shows whether this browser receives
 * push and turns it on (permission prompt from this click) or off.
 */
export function PushDeviceRow({push}: {push: PushState}) {
  const t = useT();
  const blocked = push.availability !== 'available' || push.permission === 'denied';
  return (
    <div className="set-row">
      <div className="set-text">
        <span className="set-label">{t('push.device')}</span>
        <span className={push.error ? 'field-error' : 'set-desc'} role={push.error ? 'alert' : undefined}>
          {pushStatusText(push, t)}
        </span>
      </div>
      <Switch
        checked={push.subscribed}
        disabled={blocked || push.busy}
        busy={push.busy}
        label={t('push.device')}
        onChange={next => void (next ? push.enable() : push.disable())}
      />
    </div>
  );
}
