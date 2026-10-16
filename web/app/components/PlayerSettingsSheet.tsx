import {useEffect, useState} from 'react';
import {useT} from '../lib/i18n';
import type {MessageKey} from '../lib/messages.zh-CN';
import {Icon} from './Icon';
import {Sheet, SheetTabs} from './Sheet';

export type SettingsTab = 'source' | 'quality' | 'captions' | 'audio' | 'rate';

export type Choice<T> = {value: T; label: string; hint?: string};

const TAB_KEYS: {id: SettingsTab; label: MessageKey}[] = [
  {id: 'source', label: 'player.source'},
  {id: 'quality', label: 'player.quality'},
  {id: 'captions', label: 'player.captions'},
  {id: 'audio', label: 'player.audio'},
  {id: 'rate', label: 'player.rate'},
];

function ChoiceList<T extends string | number>({label, options, value, onChange}: {
  label: string;
  options: Choice<T>[];
  value: T;
  onChange: (value: T) => void;
}) {
  return (
    <div role="radiogroup" aria-label={label} className="choice-list">
      {options.map(option => (
        <button
          key={String(option.value)}
          type="button"
          role="radio"
          aria-checked={option.value === value}
          className="choice"
          onClick={() => onChange(option.value)}
        >
          <span className="choice-text">
            <span>{option.label}</span>
            {option.hint && <span className="choice-hint">{option.hint}</span>}
          </span>
          {option.value === value && (
            <span className="choice-mark" aria-hidden="true">
              <Icon name="check" size={18} />
            </span>
          )}
        </button>
      ))}
    </div>
  );
}

/**
 * MobileSources: player settings as a bottom sheet. Tabs follow the artboard
 * (片源 / 清晰度 / 字幕 / 音轨 / 倍速). Only what the current stream exposes is
 * listed; nothing is filled with sample data.
 */
export function PlayerSettingsSheet({open, onClose, container, initialTab = 'quality', source, quality, rate, captions, audio}: {
  open: boolean;
  onClose: () => void;
  container?: HTMLElement | null;
  initialTab?: SettingsTab;
  /** versioned: options come from play-config (正片 / 高画质 / 配音 / 其他版本). */
  source: {options: Choice<number>[]; value: number; onChange: (value: number) => void; versioned: boolean};
  quality: {options: Choice<number>[]; value: number; onChange: (value: number) => void};
  rate: {options: Choice<number>[]; value: number; onChange: (value: number) => void};
  captions: {options: Choice<number>[]; value: number; onChange: (value: number) => void};
  audio: {options: Choice<number>[]; value: number; onChange: (value: number) => void};
}) {
  const t = useT();
  const TABS = TAB_KEYS.map(x => ({id: x.id, label: t(x.label)}));
  const [tab, setTab] = useState<SettingsTab>(initialTab);
  useEffect(() => {
    if (open) setTab(initialTab);
  }, [open, initialTab]);

  let body;
  switch (tab) {
    case 'source':
      body =
        source.options.length > 1 ? (
          <>
            <p className="sheet-note">
              {source.versioned ? t('player.sourcesNote') : t('player.linesNote')}
              {t('player.autoSwitchNote')}
            </p>
            <ChoiceList label={t('player.source')} options={source.options} value={source.value} onChange={source.onChange} />
          </>
        ) : (
          <p className="sheet-note">{t('player.oneSource')}</p>
        );
      break;
    case 'quality':
      body =
        quality.options.length > 1 ? (
          <ChoiceList label={t('player.quality')} options={quality.options} value={quality.value} onChange={quality.onChange} />
        ) : (
          <p className="sheet-note">{t('player.oneQuality')}</p>
        );
      break;
    case 'captions':
      body =
        captions.options.length > 1 ? (
          <ChoiceList label={t('player.captions')} options={captions.options} value={captions.value} onChange={captions.onChange} />
        ) : (
          <p className="sheet-note">{t('player.noCaptions')}</p>
        );
      break;
    case 'audio':
      body =
        audio.options.length > 1 ? (
          <ChoiceList label={t('player.audio')} options={audio.options} value={audio.value} onChange={audio.onChange} />
        ) : (
          <p className="sheet-note">{t('player.oneAudio')}</p>
        );
      break;
    case 'rate':
      body = <ChoiceList label={t('player.rate')} options={rate.options} value={rate.value} onChange={rate.onChange} />;
      break;
  }

  return (
    <Sheet
      open={open}
      onClose={onClose}
      label={t('player.settingsSheet')}
      container={container}
      className="sheet-settings"
      header={<SheetTabs tabs={TABS} value={tab} onChange={setTab} label={t('player.settingsSheet')} />}
    >
      <div role="tabpanel" aria-label={TABS.find(t => t.id === tab)?.label}>
        {body}
      </div>
    </Sheet>
  );
}
