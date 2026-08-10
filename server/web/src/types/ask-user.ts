// Types for the chat-side ask-user-question UI. Mirrors the Go DTOs in
// server/internal/api/ask_user.go exactly. The REST wrapper does NOT
// snake-to-camel convert, so these match the raw JSON shapes.
//
// Timestamps are unix milliseconds (number), same convention as the
// permission-prompt types — see types/permission.ts for the rationale.

export type AskUserRequestStatus =
  | 'pending'
  | 'answered'
  | 'timeout'
  | 'cancelled';

export interface AskUserQuestionOption {
  label: string;
  description?: string;
  /** Optional renderable preview (mockup, code snippet, comparison) shown
   *  when this option is focused. Mirrors the AskUserQuestion `preview?` field. */
  preview?: string;
  /** When true, the SPA highlights this option as Claude's suggested default
   *  (star icon + accent ring). Locale-agnostic alternative to a "(Recommended)"
   *  suffix on the label. */
  recommended?: boolean;
}

export interface AskUserQuestion {
  question: string;
  /** Short legend (≤12 chars) shown above the question — fieldset-style.
   *  Optional: backend `omitempty` on the Go side means it may be absent. */
  header?: string;
  multiSelect: boolean;
  options: AskUserQuestionOption[];
}

export interface AskUserRequest {
  id: number;
  workspace_id: number;
  session_id: string;
  questions: AskUserQuestion[];
  /** unix milliseconds (number), not ISO string */
  requested_at: number;
  /** unix milliseconds (number), not ISO string */
  expires_at: number;
  status: AskUserRequestStatus;
}

export interface AskUserAnswerBody {
  /** Verbatim question text — matches request.questions[i].question. */
  question: string;
  /** Selected option labels. Length 1 for multiSelect=false; ≥1 for multiSelect=true. */
  labels: string[];
  /** Free-form text when the user picked "Other" / wanted to elaborate. */
  notes?: string;
}

export interface AskUserDecideBody {
  answers: AskUserAnswerBody[];
}
