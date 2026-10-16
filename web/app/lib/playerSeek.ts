// A small seek API for the one hosted Player (components/PlayerHost.tsx):
// the Player registers a seeker for its release while it is mounted, and
// page parts outside it (clickable timestamps in comments and reviews) ask
// it to jump. Module-level like the host: there is one player.

type Seeker = {slug: string; seek: (seconds: number) => boolean};
let current: Seeker | null = null;

/** Called by the Player; returns the unregister function (an effect cleanup). */
export function registerSeeker(slug: string, seek: (seconds: number) => boolean): () => void {
  const entry = {slug, seek};
  current = entry;
  return () => {
    if (current === entry) current = null;
  };
}

/** Seeks the player showing `slug`; false when none is (or it cannot seek yet). */
export function seekPlayer(slug: string, seconds: number): boolean {
  return !!current && current.slug === slug && current.seek(seconds);
}
