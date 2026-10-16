import {Link} from 'react-router';
import {useConsent} from '../lib/consent';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';

// LegalPage footer. Only destinations that exist are linked: 帮助中心 / 反馈 /
// 合作 pages have no backend yet.
// TODO(tech plan 3.13 feedback): 帮助中心、反馈问题、内容合作 links.
const LEGAL_LINKS: [string, MessageKey][] = [
  ['/legal/privacy', 'footer.privacy'],
  ['/legal/terms', 'footer.terms'],
  ['/legal/cookies', 'footer.cookies'],
  ['/legal/copyright', 'footer.copyright'],
  ['/legal/community', 'footer.community'],
  ['/legal/ads', 'footer.ads'],
  ['/legal/performer-info', 'footer.performerInfo'],
];

export function Footer() {
  const t = useT();
  const {openPrefs} = useConsent();
  return (
    <footer className="site-footer">
      <nav className="footer-links" aria-label={t('footer.legal')}>
        {LEGAL_LINKS.map(([to, label]) => (
          <Link key={to} to={to} className="footer-link">
            {t(label)}
          </Link>
        ))}
        <button type="button" className="footer-link" onClick={openPrefs}>
          {t('footer.cookieSettings')}
        </button>
        <button type="button" className="footer-link" onClick={openPrefs}>
          {t('footer.doNotSell')}
        </button>
      </nav>
      <p className="footer-note">{t('footer.note')}</p>
    </footer>
  );
}
