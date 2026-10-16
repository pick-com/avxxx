import {AsyncLocalStorage} from 'node:async_hooks';
import {PassThrough} from 'node:stream';
import {createReadableStreamFromReadable} from '@react-router/node';
import {isbot} from 'isbot';
import type {RenderToPipeableStreamOptions} from 'react-dom/server';
import {renderToPipeableStream} from 'react-dom/server';
import {ServerRouter, type EntryContext} from 'react-router';
import {DEFAULT_LANG} from './lib/langs';
import {setScopeGetter, type RenderScope} from './lib/scope';
import type {RootData} from './root';

// SSR entry (PRD 13 SEO). Same as React Router's default Node entry, plus:
//   - each render runs in an AsyncLocalStorage scope holding the page's
//     language and UI dictionary, read by t() / formatters (lib/scope.ts), so
//     concurrent renders in different languages never share module state;
//   - crawlers (isbot) get the whole document at once (onAllReady);
//   - HTML is never cached by shared caches: an unprefixed URL may answer a
//     302 that depends on the `lang` cookie (root loader).

export const streamTimeout = 5_000;

const scope = new AsyncLocalStorage<RenderScope>();
setScopeGetter(() => scope.getStore());

export default function handleRequest(request: Request, responseStatusCode: number, responseHeaders: Headers, routerContext: EntryContext) {
  if (!responseHeaders.has('Cache-Control')) responseHeaders.set('Cache-Control', 'no-cache');
  responseHeaders.append('Vary', 'Cookie');

  if (request.method.toUpperCase() === 'HEAD') {
    return new Response(null, {status: responseStatusCode, headers: responseHeaders});
  }

  const root = routerContext.staticHandlerContext.loaderData?.root as RootData | undefined;
  const store: RenderScope = {lang: root?.lang ?? DEFAULT_LANG, dict: root?.dict ?? {}};

  return scope.run(
    store,
    () =>
      new Promise<Response>((resolve, reject) => {
        let shellRendered = false;
        const userAgent = request.headers.get('user-agent');
        const readyOption: keyof RenderToPipeableStreamOptions = (userAgent && isbot(userAgent)) || routerContext.isSpaMode ? 'onAllReady' : 'onShellReady';
        let timeoutId: ReturnType<typeof setTimeout> | undefined = setTimeout(() => abort(), streamTimeout + 1000);

        const {pipe, abort} = renderToPipeableStream(<ServerRouter context={routerContext} url={request.url} />, {
          [readyOption]() {
            shellRendered = true;
            const body = new PassThrough({
              final(callback) {
                clearTimeout(timeoutId);
                timeoutId = undefined;
                callback();
              },
            });
            const stream = createReadableStreamFromReadable(body);
            responseHeaders.set('Content-Type', 'text/html; charset=utf-8');
            pipe(body);
            resolve(new Response(stream, {headers: responseHeaders, status: responseStatusCode}));
          },
          onShellError(error: unknown) {
            reject(error);
          },
          onError(error: unknown) {
            responseStatusCode = 500;
            if (shellRendered) console.error(error);
          },
        });
      }),
  );
}
