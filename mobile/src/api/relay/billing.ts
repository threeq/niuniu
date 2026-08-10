/**
 * Relay billing API — change-plan wrapper.
 *
 * Full checkout flow:
 *  1. Call changePlan(client, targetPlanID).
 *  2. If result.kind === 'checkout', open result.url in the system browser
 *     (e.g. via expo-linking or expo-web-browser).
 *  3. Stripe handles the payment form.
 *  4. On success Stripe redirects the user to the success_url configured on the relay.
 *  5. In the background Stripe fires a webhook to the relay (/api/webhooks/billing).
 *  6. The relay webhook handler upgrades the account plan.
 *  7. The mobile app should poll GET /api/billing/my-plan (or listen for an SSE event)
 *     to detect when the plan has been upgraded and update the UI.
 *
 * For free plans the change is applied immediately and result.kind === 'applied'.
 */

import { RelayApiError, type RelayClient } from './client';

/** Returned when a free plan change is applied on the server without checkout. */
export interface PlanAppliedResult {
  kind: 'applied';
}

/** Returned when a paid plan requires Stripe Checkout. */
export interface CheckoutResult {
  kind: 'checkout';
  /** Stripe-hosted checkout URL. Open this in the system browser. */
  url: string;
  targetPlanID: string;
}

export type ChangePlanResult = PlanAppliedResult | CheckoutResult;

interface ChangePlanResponse {
  applied?: boolean;
  checkout_url?: string;
  target_plan_id?: string;
}

/**
 * Request a plan change for the authenticated account.
 *
 * @param client - Authenticated RelayClient instance.
 * @param targetPlanID - The plan_id to switch to (e.g. "pro", "free").
 * @returns A ChangePlanResult indicating whether the change was applied
 *          immediately or requires the user to complete Stripe Checkout.
 */
export async function changePlan(
  client: RelayClient,
  targetPlanID: string,
): Promise<ChangePlanResult> {
  // Route through client.request so the 401 auto-refresh path is shared
  // with the rest of the relay API surface (no more silent sign-outs
  // when the access token expires mid-checkout).
  let body: ChangePlanResponse | undefined;
  try {
    body = await client.request<ChangePlanResponse>('/api/billing/change-plan', {
      method: 'POST',
      body: JSON.stringify({ target_plan_id: targetPlanID }),
    });
  } catch (err) {
    if (err instanceof RelayApiError) {
      throw new Error(`changePlan failed: ${err.code} (${err.status})`);
    }
    throw err;
  }

  // client.request returns undefined for a 2xx with an empty body — the
  // relay always returns JSON for this endpoint, so an empty response
  // is malformed. Fail loudly so the caller can surface the issue
  // rather than silently treating it as "applied".
  if (body == null) {
    throw new Error(
      'changePlan failed: empty response body from /api/billing/change-plan',
    );
  }

  if (body.checkout_url) {
    return {
      kind: 'checkout',
      url: body.checkout_url,
      targetPlanID: body.target_plan_id ?? targetPlanID,
    };
  }

  return { kind: 'applied' };
}
