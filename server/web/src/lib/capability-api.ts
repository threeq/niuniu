// REST client for the operable capability description (Epic #726 / #738).
//
// Backend: server/internal/server/handler_capability.go. Success responses are
// wrapped in a `{data: ...}` envelope; this client unwraps it.

import { api } from './api';
import type { CapabilityResponse, CapabilityTargetKind } from '@/types/capability';

interface Envelope {
  data: CapabilityResponse;
}

export const capabilityApi = {
  get: (kind: CapabilityTargetKind, ref: string | number): Promise<CapabilityResponse> =>
    api
      .get<Envelope>(
        `/me/capabilities/${kind}/${encodeURIComponent(String(ref))}`,
      )
      .then((r) => r.data),
};
