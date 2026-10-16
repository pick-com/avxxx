import {Link, useLocation} from '../lib/nav';
import {useConsent} from '../lib/consent';
import {partnershipEmail} from '../lib/compliance';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';

// LegalPage footer: the legal documents, cookie choices, then 帮助中心 /
// 反馈问题 / 内容合作. 合作 is a mailto to VITE_PARTNERSHIP_EMAIL and is
// hidden when that is not configured. The feedback link carries the current
// path (?from=) so the form can offer to attach it.
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
  const {pathname} = useLocation();
  const partner = partnershipEmail();
  const here = pathname;
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
        <Link to="/help" className="footer-link">
          {t('footer.help')}
        </Link>
        <Link to={here.startsWith('/feedback') ? '/feedback' : `/feedback?from=${encodeURIComponent(here)}`} className="footer-link">
          {t('footer.feedback')}
        </Link>
        {partner && (
          <a href={`mailto:${partner}`} className="footer-link">
            {t('footer.partnership')}
          </a>
        )}
      </nav>
      <p className="footer-note">{t('footer.note')}</p>
    </footer>
  );
}
