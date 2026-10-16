// 收藏夹 helpers shared by the favorites page, the release page picker and
// the public playlist page (PRD 7 NavFavorites; tech plan 3.13).

import {playlistSharePath, type Playlist, type PlaylistVisibility} from './api';
import type {IconName} from '../components/Icon';
import {t} from './i18n';
import type {MessageKey} from './messages.zh-CN';

/** Stored name of the default playlist (store.DefaultPlaylistName). */
const DEFAULT_NAME = '默认收藏夹';

/** The display name: the default playlist keeps a localized label until renamed. */
export function playlistName(p: Pick<Playlist, 'name' | 'is_default'>): string {
  return p.is_default && p.name === DEFAULT_NAME ? t('playlists.defaultName') : p.name;
}

export const visibilityLabel: Record<PlaylistVisibility, MessageKey> = {
  private: 'playlists.visPrivate',
  link: 'playlists.visLink',
  public: 'playlists.visPublic',
};

export const visibilityHint: Record<PlaylistVisibility, MessageKey> = {
  private: 'playlists.visPrivateHint',
  link: 'playlists.visLinkHint',
  public: 'playlists.visPublicHint',
};

export const visibilityIcon: Record<PlaylistVisibility, IconName> = {
  private: 'lock',
  link: 'share',
  public: 'globe',
};

/** Absolute share URL for the current origin (browser only). */
export function shareUrl(token: string): string {
  const origin = typeof window === 'undefined' ? '' : window.location.origin;
  return origin + playlistSharePath(token);
}
