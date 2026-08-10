import { changePlan } from '../billing';
import { RelayApiError, type RelayClient } from '../client';

type RequestFn = (path: string, opts?: RequestInit & { skipAuth?: boolean }) => Promise<unknown>;

const makeClient = (requestImpl: RequestFn): RelayClient => {
  // Minimal fake — only the methods changePlan consumes. Cast via unknown
  // because RelayClient.request is generic and the mock can't infer T.
  return {
    baseUrl: 'https://relay.example.com',
    request: requestImpl,
  } as unknown as RelayClient;
};

describe('billing.changePlan', () => {
  it('POSTs to /api/billing/change-plan with the target plan id', async () => {
    const spy = jest.fn<Promise<unknown>, [string, RequestInit?]>(async () => ({
      applied: true,
    }));
    await changePlan(makeClient(spy as unknown as RequestFn), 'pro');

    expect(spy).toHaveBeenCalledTimes(1);
    expect(spy).toHaveBeenCalledWith(
      '/api/billing/change-plan',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify({ target_plan_id: 'pro' }),
      }),
    );
  });

  it('returns {kind: "applied"} for a free-plan change with no checkout_url', async () => {
    const client = makeClient(async () => ({ applied: true }));
    const result = await changePlan(client, 'free');
    expect(result).toEqual({ kind: 'applied' });
  });

  it('returns {kind: "checkout"} with the Stripe URL when checkout_url is present', async () => {
    const client = makeClient(async () => ({
      checkout_url: 'https://stripe.example.com/session/abc',
      target_plan_id: 'pro',
    }));
    const result = await changePlan(client, 'pro');
    expect(result).toEqual({
      kind: 'checkout',
      url: 'https://stripe.example.com/session/abc',
      targetPlanID: 'pro',
    });
  });

  it('falls back to the caller-supplied target plan id when server omits it', async () => {
    const client = makeClient(async () => ({
      checkout_url: 'https://stripe.example.com/session/xyz',
    }));
    const result = await changePlan(client, 'pro');
    expect(result).toEqual({
      kind: 'checkout',
      url: 'https://stripe.example.com/session/xyz',
      targetPlanID: 'pro',
    });
  });

  it('maps RelayApiError to a human-readable Error preserving code + status', async () => {
    const client = makeClient(async () => {
      throw new RelayApiError(404, 'plan_not_found', 'HTTP 404: plan_not_found');
    });
    await expect(changePlan(client, 'enterprise')).rejects.toThrow(
      'changePlan failed: plan_not_found (404)',
    );
  });

  it('passes through non-RelayApiError exceptions unchanged (e.g. network errors)', async () => {
    const networkErr = new TypeError('Network request failed');
    const client = makeClient(async () => {
      throw networkErr;
    });
    await expect(changePlan(client, 'pro')).rejects.toBe(networkErr);
  });

  it('throws a descriptive error when the server responds 2xx with an empty body', async () => {
    // RelayClient.request returns `undefined as T` for an empty body.
    // Treating that as "applied" would hide a real server regression,
    // so changePlan must surface the malformed response explicitly.
    const client = makeClient(async () => undefined);
    await expect(changePlan(client, 'pro')).rejects.toThrow(
      'changePlan failed: empty response body',
    );
  });
});
