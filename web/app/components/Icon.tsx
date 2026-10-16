// Stroke icons taken from the REEL artboards (TopBar, SideNav, MobileHome).
const paths = {
  menu: 'M4 6h16M4 12h16M4 18h16',
  search: 'M11 18a7 7 0 100-14 7 7 0 000 14zM20 20l-4-4',
  sun: 'M12 4V2M12 22v-2M4 12H2M22 12h-2M5.6 5.6L4.2 4.2M19.8 19.8l-1.4-1.4M5.6 18.4l-1.4 1.4M19.8 4.2l-1.4 1.4M12 17a5 5 0 100-10 5 5 0 000 10z',
  moon: 'M21 13A9 9 0 1111 3a7 7 0 0010 10z',
  auto: 'M12 21a9 9 0 100-18 9 9 0 000 18zM12 3v18',
  globe: 'M12 21a9 9 0 100-18 9 9 0 000 18zM3 12h18M12 3c3 3 3 15 0 18M12 3c-3 3-3 15 0 18',
  home: 'M3 11l9-7 9 7v9a1 1 0 01-1 1h-5v-6h-6v6H4a1 1 0 01-1-1z',
  new: 'M12 3v5M12 16v5M3 12h5M16 12h5M6 6l3 3M15 15l3 3M18 6l-3 3M9 15l-3 3',
  categories: 'M4 4h7v7H4zM13 4h7v7h-7zM4 13h7v7H4zM13 13h7v7h-7z',
  actors: 'M12 3l2.6 5.6 6.1.7-4.5 4.2 1.2 6L12 16.6 6.6 19.5l1.2-6-4.5-4.2 6.1-.7z',
  history: 'M3 12a9 9 0 103-6.7M3 4v5h5M12 8v5l3 2',
  favorites: 'M6 3h12v18l-6-4-6 4z',
  discover: 'M12 21a9 9 0 100-18 9 9 0 000 18zM15.5 8.5l-2 5-5 2 2-5z',
  following: 'M4 7h16M6 4h12M3 10h18v10H3zM10 13l5 2.5-5 2.5z',
  me: 'M12 12a4 4 0 100-8 4 4 0 000 8zM4 21c1-4 4-6 8-6s7 2 8 6',
  back: 'M15 5l-7 7 7 7',
  play: 'M7 4l13 8-13 8z',
  close: 'M6 6l12 12M18 6L6 18',
  share: 'M12 3v12M7 8l5-5 5 5M5 14v6h14v-6',
  film: 'M4 6h16v12H4zM10 9l5 3-5 3z',
  alert: 'M12 3l10 18H2zM12 10v5M12 18v.01',
  empty: 'M4 7h16v13H4zM4 7l2-4h12l2 4M9 12h6',
} as const;

export type IconName = keyof typeof paths;

export function Icon({name, size = 20, filled = false}: {name: IconName; size?: number; filled?: boolean}) {
  return (
    <svg width={size} height={size} viewBox="0 0 24 24" aria-hidden="true" focusable="false">
      <path
        d={paths[name]}
        fill={filled ? 'currentColor' : 'none'}
        stroke="currentColor"
        strokeWidth={1.8}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
    </svg>
  );
}
