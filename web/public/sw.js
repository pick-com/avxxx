/* REEL service worker: Web Push only (PRD 17, tech plan 3.7).
 *
 * Served from the site root (web/public/sw.js → dist/client/sw.js) so its
 * scope is "/". It does not intercept fetches: no offline cache, nothing
 * that could serve a stale app shell.
 *
 * push: the payload is internal/notify.PushMessage {title, body, url, tag,
 * count}. Notifications with the same tag replace each other (one merged
 * "new works" notice per kind), renotify makes the replacement audible.
 *
 * notificationclick: focus a tab already on that URL, else navigate an open
 * REEL tab there, else open a new window. Only same-origin URLs are opened.
 */

self.addEventListener('install', () => {
  self.skipWaiting();
});

self.addEventListener('activate', event => {
  event.waitUntil(self.clients.claim());
});

function sameOriginUrl(raw) {
  try {
    const url = new URL(raw || '/', self.location.origin);
    return url.origin === self.location.origin ? url.href : self.location.origin + '/';
  } catch (e) {
    return self.location.origin + '/';
  }
}

self.addEventListener('push', event => {
  let data = {};
  try {
    data = event.data ? event.data.json() : {};
  } catch (e) {
    data = {body: event.data ? event.data.text() : ''};
  }
  const title = typeof data.title === 'string' && data.title ? data.title : 'REEL';
  const tag = typeof data.tag === 'string' && data.tag ? data.tag : 'reel';
  const options = {
    body: typeof data.body === 'string' ? data.body : '',
    tag,
    renotify: true,
    data: {url: sameOriginUrl(data.url), count: typeof data.count === 'number' ? data.count : 1},
  };
  event.waitUntil(self.registration.showNotification(title, options));
});

self.addEventListener('notificationclick', event => {
  event.notification.close();
  const target = sameOriginUrl(event.notification.data && event.notification.data.url);
  event.waitUntil(
    (async () => {
      const windows = await self.clients.matchAll({type: 'window', includeUncontrolled: true});
      const exact = windows.find(c => c.url === target);
      if (exact) return exact.focus();
      const open = windows.find(c => c.url.startsWith(self.location.origin));
      if (open && 'navigate' in open) {
        try {
          const moved = await open.navigate(target);
          if (moved) return moved.focus();
        } catch (e) {
          /* uncontrolled client: fall through to a new window */
        }
      }
      return self.clients.openWindow(target);
    })(),
  );
});
