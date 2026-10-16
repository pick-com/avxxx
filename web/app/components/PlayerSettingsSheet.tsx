import {useEffect, useState} from 'react';
import {Icon} from './Icon';
import {Sheet, SheetTabs} from './Sheet';

export type SettingsTab = 'source' | 'quality' | 'captions' | 'audio' | 'rate';

export type Choice<T> = {value: T; label: string; hint?: string};

const TABS: {id: SettingsTab; label: string}[] = [
  {id: 'source', label: '片源'},
  {id: 'quality', label: '清晰度'},
  {id: 'captions', label: '字幕'},
  {id: 'audio', label: '音轨'},
  {id: 'rate', label: '倍速'},
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
              {source.versioned ? '切换片源会保留播放进度；时长不同的版本从头播放。' : '切换线路会保留播放进度。'}
              播放失败时自动换到下一个可用片源。
            </p>
            <ChoiceList label="片源" options={source.options} value={source.value} onChange={source.onChange} />
          </>
        ) : (
          <p className="sheet-note">当前只有一个片源。播放失败时会提示并可重试。</p>
        );
      break;
    case 'quality':
      body =
        quality.options.length > 1 ? (
          <ChoiceList label="清晰度" options={quality.options} value={quality.value} onChange={quality.onChange} />
        ) : (
          <p className="sheet-note">当前片源只提供一种清晰度。</p>
        );
      break;
    case 'captions':
      body =
        captions.options.length > 1 ? (
          <ChoiceList label="字幕" options={captions.options} value={captions.value} onChange={captions.onChange} />
        ) : (
          <p className="sheet-note">这部作品暂无字幕。</p>
        );
      break;
    case 'audio':
      body =
        audio.options.length > 1 ? (
          <ChoiceList label="音轨" options={audio.options} value={audio.value} onChange={audio.onChange} />
        ) : (
          <p className="sheet-note">当前片源只有一条音轨。</p>
        );
      break;
    case 'rate':
      body = <ChoiceList label="倍速" options={rate.options} value={rate.value} onChange={rate.onChange} />;
      break;
  }

  return (
    <Sheet
      open={open}
      onClose={onClose}
      label="播放设置"
      container={container}
      className="sheet-settings"
      header={<SheetTabs tabs={TABS} value={tab} onChange={setTab} label="播放设置" />}
    >
      <div role="tabpanel" aria-label={TABS.find(t => t.id === tab)?.label}>
        {body}
      </div>
    </Sheet>
  );
}
