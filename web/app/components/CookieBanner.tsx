import {useEffect, useState} from 'react';
import {Link} from '../lib/nav';
import {useConsent, type ConsentChoice} from '../lib/consent';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {Icon} from './Icon';
import {Sheet} from './Sheet';
import {Switch} from './Switch';

// CookieBanner (desktop) / MobileConsent (phone: the same banner pinned above
// the tab bar, preferences as a bottom sheet). Shown on first visit; until the
// visitor chooses only necessary storage is used (there is no analytics or ad
// script yet, so nothing else has to be held back). Every choice is also
// recorded server-side (lib/consent: consent_records).

type Cat = {key: keyof ConsentChoice | 'necessary'; title: MessageKey; desc: MessageKey};
const CATS: Cat[] = [
  {key: 'necessary', title: 'cookie.necessary', desc: 'cookie.necessaryDesc'},
  {key: 'analytics', title: 'cookie.analytics', desc: 'cookie.analyticsDesc'},
  {key: 'personalized_ads', title: 'cookie.ads', desc: 'cookie.adsDesc'},
  {key: 'personalized_recs', title: 'cookie.recs', desc: 'cookie.recsDesc'},
];

const ALL_ON: ConsentChoice = {analytics: true, personalized_ads: true, personalized_recs: true};
const ALL_OFF: ConsentChoice = {analytics: false, personalized_ads: false, personalized_recs: false};

export function CookieBanner() {
  const t = useT();
  const {choice, ready, gpc, prefsOpen, openPrefs, closePrefs, save} = useConsent();
  const [draft, setDraft] = useState<ConsentChoice>(ALL_OFF);

  useEffect(() => {
    if (prefsOpen) setDraft(choice ? {analytics: choice.analytics, personalized_ads: choice.personalized_ads && !gpc, personalized_recs: choice.personalized_recs} : {...ALL_OFF});
  }, [prefsOpen, choice, gpc]);

  const showBanner = ready && !choice && !prefsOpen;

  return (
    <>
      {showBanner && (
        <section className="consent-banner" role="region" aria-label={t('cookie.region')}>
          <div className="consent-text">
            <strong>{t('cookie.bannerTitle')}</strong>
            <span>
              {t('cookie.bannerBody')}
              <Link to="/legal/cookies" className="meta-link">
                {t('footer.cookies')}
              </Link>
            </span>
            {gpc && <span className="consent-gpc">{t('cookie.gpc')}</span>}
          </div>
          <div className="consent-actions">
            <button type="button" className="btn btn-secondary" onClick={openPrefs}>
              {t('cookie.custom')}
            </button>
            <button type="button" className="btn btn-secondary" onClick={() => save(ALL_OFF, 'banner')}>
              {t('cookie.necessaryOnly')}
            </button>
            <button type="button" className="btn btn-primary" onClick={() => save(ALL_ON, 'banner')}>
              {t('cookie.acceptAll')}
            </button>
          </div>
        </section>
      )}
      <Sheet open={prefsOpen} onClose={closePrefs} label={t('cookie.choose')} initialSnap="full" className="sheet-consent" header={<h2 className="sheet-title">{t('cookie.choose')}</h2>}>
        <div className="consent-prefs">
          {gpc && (
            <p className="consent-gpc">
              <Icon name="lock" size={16} /> {t('cookie.gpc')}
            </p>
          )}
          {CATS.map(cat => {
            const locked = cat.key === 'necessary' || (cat.key === 'personalized_ads' && gpc);
            const on = cat.key === 'necessary' ? true : cat.key === 'personalized_ads' && gpc ? false : draft[cat.key];
            return (
              <div key={cat.key} className="set-row">
                <div className="set-text">
                  <span className="set-label" id={`consent-${cat.key}`}>
                    {t(cat.title)}
                  </span>
                  <span className="set-desc">
                    {t(cat.desc)}
                    {cat.key === 'necessary' && t('cookie.alwaysOn')}
                  </span>
                </div>
                <Switch
                  checked={on}
                  disabled={locked}
                  label={t(cat.title)}
                  onChange={next => {
                    if (cat.key !== 'necessary') setDraft(d => ({...d, [cat.key]: next}));
                  }}
                />
              </div>
            );
          })}
          <div className="consent-actions consent-actions-sheet">
            <button type="button" className="btn btn-secondary" onClick={() => save(ALL_OFF)}>
              {t('cookie.rejectAll')}
            </button>
            <button type="button" className="btn btn-primary" onClick={() => save(draft)}>
              {t('cookie.save')}
            </button>
          </div>
        </div>
      </Sheet>
    </>
  );
}
