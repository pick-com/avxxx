import {useEffect, useState} from 'react';
import {Link} from 'react-router';
import {useConsent, type ConsentChoice} from '../lib/consent';
import {Icon} from './Icon';
import {Sheet} from './Sheet';
import {Switch} from './Switch';

// CookieBanner (desktop) / MobileConsent (phone: the same banner pinned above
// the tab bar, preferences as a bottom sheet). Shown on first visit; until the
// visitor chooses only necessary storage is used (there is no analytics or ad
// script yet, so nothing else has to be held back).

type Cat = {key: keyof ConsentChoice | 'necessary'; title: string; desc: string};
const CATS: Cat[] = [
  {key: 'necessary', title: '必要', desc: '登录、记住播放进度、安全防护、记住你在这里的选择。没有它们网站无法使用。'},
  {key: 'analytics', title: '分析', desc: '统计播放卡顿、页面加载速度和功能使用情况，用来改进产品。数据不用于广告。'},
  {key: 'personalized_ads', title: '个性化广告', desc: '根据你的观看兴趣挑选广告。关闭后广告数量不变，只按正在看的视频类型投放。'},
  {key: 'personalized_recs', title: '个性化推荐', desc: '根据观看记录推荐视频和演员。关闭后首页按热门和新上线排序。'},
];

const ALL_ON: ConsentChoice = {analytics: true, personalized_ads: true, personalized_recs: true};
const ALL_OFF: ConsentChoice = {analytics: false, personalized_ads: false, personalized_recs: false};

export function CookieBanner() {
  const {choice, ready, gpc, prefsOpen, openPrefs, closePrefs, save} = useConsent();
  const [draft, setDraft] = useState<ConsentChoice>(ALL_OFF);

  useEffect(() => {
    if (prefsOpen) setDraft(choice ? {analytics: choice.analytics, personalized_ads: choice.personalized_ads && !gpc, personalized_recs: choice.personalized_recs} : {...ALL_OFF});
  }, [prefsOpen, choice, gpc]);

  const showBanner = ready && !choice && !prefsOpen;

  return (
    <>
      {showBanner && (
        <section className="consent-banner" role="region" aria-label="Cookie 同意">
          <div className="consent-text">
            <strong>我们用 Cookie 让网站正常运行</strong>
            <span>
              必要 Cookie 用于登录和播放，不能关闭。经你同意后，我们还会用 Cookie 了解大家怎么使用网站，并展示更相关的广告。你可以随时在页脚「Cookie 设置」里修改。
              <Link to="/legal/cookies" className="meta-link">
                Cookie 政策
              </Link>
            </span>
            {gpc && <span className="consent-gpc">检测到你的浏览器开启了「全局隐私控制（GPC）」，个性化广告已按你的设置关闭。</span>}
          </div>
          <div className="consent-actions">
            <button type="button" className="btn btn-secondary" onClick={openPrefs}>
              自己选择
            </button>
            <button type="button" className="btn btn-secondary" onClick={() => save(ALL_OFF)}>
              只用必要的
            </button>
            <button type="button" className="btn btn-primary" onClick={() => save(ALL_ON)}>
              全部接受
            </button>
          </div>
        </section>
      )}
      <Sheet open={prefsOpen} onClose={closePrefs} label="选择 Cookie" initialSnap="full" className="sheet-consent" header={<h2 className="sheet-title">选择 Cookie</h2>}>
        <div className="consent-prefs">
          {gpc && (
            <p className="consent-gpc">
              <Icon name="lock" size={16} /> 检测到你的浏览器开启了「全局隐私控制（GPC）」，个性化广告已按你的设置关闭。
            </p>
          )}
          {CATS.map(cat => {
            const locked = cat.key === 'necessary' || (cat.key === 'personalized_ads' && gpc);
            const on = cat.key === 'necessary' ? true : cat.key === 'personalized_ads' && gpc ? false : draft[cat.key];
            return (
              <div key={cat.key} className="set-row">
                <div className="set-text">
                  <span className="set-label" id={`consent-${cat.key}`}>
                    {cat.title}
                  </span>
                  <span className="set-desc">
                    {cat.desc}
                    {cat.key === 'necessary' && '（始终开启）'}
                  </span>
                </div>
                <Switch
                  checked={on}
                  disabled={locked}
                  label={cat.title}
                  onChange={next => {
                    if (cat.key !== 'necessary') setDraft(d => ({...d, [cat.key]: next}));
                  }}
                />
              </div>
            );
          })}
          <div className="consent-actions consent-actions-sheet">
            <button type="button" className="btn btn-secondary" onClick={() => save(ALL_OFF)}>
              全部拒绝
            </button>
            <button type="button" className="btn btn-primary" onClick={() => save(draft)}>
              保存选择
            </button>
          </div>
        </div>
      </Sheet>
    </>
  );
}
