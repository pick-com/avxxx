import {useEffect} from 'react';
import type {MetaFunction} from 'react-router';
import {Link} from '../lib/nav';
import {useConsent} from '../lib/consent';
import {partnershipEmail} from '../lib/compliance';
import {t, useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {seoMeta} from '../lib/seo';
import '../styles/compliance.css';

// 帮助中心 (LegalPage footer 帮助): entry points to the pages that answer
// the common questions, then the feedback form. No help-article backend
// exists; every link goes to a real page.

export const meta: MetaFunction = ({matches}) => seoMeta({matches, path: '/help', title: t('title.help'), description: t('help.intro')});

const TOPICS: {to: string; title: MessageKey; body: MessageKey}[] = [
  {to: '/legal/privacy', title: 'help.privacy', body: 'help.privacyBody'},
  {to: '/feedback?category=playback', title: 'help.playback', body: 'help.playbackBody'},
  {to: '/copyright', title: 'help.copyright', body: 'help.copyrightBody'},
  {to: '/legal/performer-info#request-form', title: 'help.performer', body: 'help.performerBody'},
  {to: '/legal/community', title: 'help.community', body: 'help.communityBody'},
];

export default function HelpPage() {
  const t = useT();
  const {openPrefs} = useConsent();
  const partner = partnershipEmail();

  useEffect(() => {
    document.title = t('title.help');
  }, []);

  return (
    <div className="page support-page">
      <h1 className="page-title">{t('help.title')}</h1>
      <p className="muted">{t('help.intro')}</p>
      <nav className="support-links" aria-label={t('help.title')}>
        {TOPICS.map(x => (
          <Link key={x.to} to={x.to} className="support-link">
            <strong>{t(x.title)}</strong>
            <span className="muted">{t(x.body)}</span>
          </Link>
        ))}
        <button type="button" className="support-link support-link-btn" onClick={openPrefs}>
          <strong>{t('help.cookies')}</strong>
          <span className="muted">{t('help.cookiesBody')}</span>
        </button>
      </nav>
      <section className="legal-other">
        <h2>{t('help.contact')}</h2>
        <span className="muted">{t('help.contactBody')}</span>
        <Link to="/feedback">{t('footer.feedback')}</Link>
      </section>
      {partner && (
        <section className="legal-other">
          <h2>{t('footer.partnership')}</h2>
          <span className="muted">{t('help.partnershipBody', {email: partner})}</span>
          <a href={`mailto:${partner}`}>{partner}</a>
        </section>
      )}
    </div>
  );
}
