import {useEffect, useRef, useState} from 'react';
import {useT} from '../lib/i18n';
import {useOnline} from '../lib/useOnline';
import {Icon} from './Icon';

const RECONNECTED_MS = 2000;

/**
 * States 05: offline banner under the top bar. It sits in the flow (sticky), so
 * it never covers content; the player keeps playing what it has buffered.
 * After reconnecting it turns green with 「已重新连接」 for 2 s, then goes away.
 */
export function NetworkBanner() {
  const t = useT();
  const online = useOnline();
  const [reconnected, setReconnected] = useState(false);
  const [stillOffline, setStillOffline] = useState(false);
  const wasOffline = useRef(false);

  useEffect(() => {
    if (!online) {
      wasOffline.current = true;
      setReconnected(false);
      return;
    }
    if (!wasOffline.current) return;
    wasOffline.current = false;
    setStillOffline(false);
    setReconnected(true);
    const timer = window.setTimeout(() => setReconnected(false), RECONNECTED_MS);
    return () => window.clearTimeout(timer);
  }, [online]);

  useEffect(() => {
    if (!stillOffline) return;
    const timer = window.setTimeout(() => setStillOffline(false), RECONNECTED_MS);
    return () => window.clearTimeout(timer);
  }, [stillOffline]);

  // 立即重试: the browser decides connectivity, so re-read it; a reconnect is
  // picked up by the online event anyway.
  const retry = () => {
    if (navigator.onLine) window.dispatchEvent(new Event('online'));
    else setStillOffline(true);
  };

  return (
    <div className="net-banner-slot" role="status" aria-live="polite">
      {!online ? (
        <div className="net-banner banner banner-warn">
          <Icon name="offline" size={18} />
          <span className="banner-text">{stillOffline ? t('net.stillOffline') : t('net.offline')}</span>
          <button type="button" className="banner-action" onClick={retry}>
            {t('net.retry')}
          </button>
        </div>
      ) : reconnected ? (
        <div className="net-banner banner banner-ok">
          <Icon name="check" size={18} />
          <span className="banner-text">{t('net.reconnected')}</span>
        </div>
      ) : null}
    </div>
  );
}
