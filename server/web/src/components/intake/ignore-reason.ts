export const IGNORE_REASON_CODES = ['irrelevant', 'duplicate', 'wrong_project', 'other'] as const
export type IgnoreReasonCode = (typeof IGNORE_REASON_CODES)[number]

export interface IgnoreReason {
  code: IgnoreReasonCode
  text: string
}
