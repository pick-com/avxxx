// Source credits for route loaders (server rendering): the page's credits are
// part of the HTML, so DMM's required notice is there without JavaScript.
import {serverApi} from './api.server';
import {attributionsPath, type Attribution, type CreditScope} from './evidence';

/** GET /api/attributions for the scope; any failure gives no credits (the page still renders). */
export const serverCredits = (scope: CreditScope, signal?: AbortSignal): Promise<Attribution[]> =>
  serverApi<{items: Attribution[] | null}>(attributionsPath(scope), {signal}).then(
    d => d.items ?? [],
    () => [],
  );
