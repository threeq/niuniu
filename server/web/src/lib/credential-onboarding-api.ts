import { api } from '@/lib/api';

// REST wrapper for the generic one-time credential-link endpoints (issue #727).
// api.ts already prepends /api. The token in the path IS the authorization, so
// these calls carry no session.

/** One field the submission page renders. Never contains a value. */
export interface CredentialFieldSpec {
  name: string;
  label: string;
  secret: boolean;
  required: boolean;
  placeholder?: string;
}

export type CredentialKind = 'provider_credential' | 'data_source';

export interface CredentialLinkInfo {
  kind: CredentialKind;
  title: string;
  fields: CredentialFieldSpec[];
}

export interface CredentialSubmitResult {
  ok: boolean;
  kind: CredentialKind;
  credential_id?: number;
  source_id?: number;
  name?: string;
  provider?: string;
  alias?: string;
}

export const credentialOnboardingApi = {
  /** Fetch the non-secret descriptor. 410 = invalid/expired/used token. */
  getInfo: (token: string): Promise<CredentialLinkInfo> =>
    api.get<CredentialLinkInfo>(`/credential-onboarding/${token}`),

  /** Submit the values typed on the page. The response never echoes them. */
  submit: (token: string, values: Record<string, string>): Promise<CredentialSubmitResult> =>
    api.post<CredentialSubmitResult>(`/credential-onboarding/${token}`, values),
};
