import { useEffect, useState } from 'react';
import { useTranslation } from 'react-i18next';
import type { TFunction } from 'i18next';
import { HelpCircle, Star } from 'lucide-react';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import type {
  AskUserRequest,
  AskUserDecideBody,
  AskUserAnswerBody,
} from '@/types/ask-user';

interface Props {
  request: AskUserRequest;
  onDecide: (body: AskUserDecideBody) => void;
}

// Internal sentinel for the always-present "Other" choice. The server
// strips this before persisting / forwarding to Claude (see
// askUserOtherSentinel in server/internal/api/ask_user.go).
const OTHER_SENTINEL = '__other__';

// AskUserQuestionCard renders one ask-user request inline in chat. Mirrors
// the official AskUserQuestion behavior:
//   - header rendered ABOVE the question as a fieldset legend
//   - radio (single-select) or checkbox (multi-select) over options
//   - every question has an implicit "Other" choice with free-text input
//   - options carrying `recommended: true` get a star + accent ring
//   - options with a `preview` field render the preview block when focused
//   - Submit is enabled only when every question has at least one selection
//     OR an "Other" text the user has typed
export function AskUserQuestionCard({ request, onDecide }: Props) {
  const { t } = useTranslation();
  const [selections, setSelections] = useState<Record<string, Set<string>>>(
    () => Object.fromEntries(request.questions.map((q) => [q.question, new Set()])),
  );
  const [otherText, setOtherText] = useState<Record<string, string>>(
    () => Object.fromEntries(request.questions.map((q) => [q.question, ''])),
  );
  const [previewKey, setPreviewKey] = useState<Record<string, string | null>>(
    () => Object.fromEntries(request.questions.map((q) => [q.question, null])),
  );
  const [remaining, setRemaining] = useState(() => msUntil(request.expires_at));

  useEffect(() => {
    const id = setInterval(() => setRemaining(msUntil(request.expires_at)), 1000);
    return () => clearInterval(id);
  }, [request.expires_at]);

  const isPending = request.status === 'pending';

  if (!isPending) {
    return (
      <Card className="my-2 border-dashed" data-ask-user-card={request.status}>
        <CardContent className="py-2 text-xs text-warm-text-muted">
          {labelForStatus(request.status, t)}
        </CardContent>
      </Card>
    );
  }

  const isAnswered = (q: string) => {
    const set = selections[q] ?? new Set<string>();
    if (set.size === 0) return false;
    if (set.has(OTHER_SENTINEL) && set.size === 1) {
      return (otherText[q] ?? '').trim().length > 0;
    }
    return true;
  };
  const allAnswered = request.questions.every((q) => isAnswered(q.question));

  const toggle = (question: string, label: string, multi: boolean) => {
    setSelections((prev) => {
      const next = { ...prev };
      const set = new Set(next[question] ?? []);
      if (multi) {
        if (set.has(label)) set.delete(label);
        else set.add(label);
      } else {
        set.clear();
        set.add(label);
      }
      next[question] = set;
      return next;
    });
    setPreviewKey((prev) => ({ ...prev, [question]: label }));
  };

  const handleSubmit = () => {
    const answers: AskUserAnswerBody[] = request.questions.map((q) => {
      const set = selections[q.question] ?? new Set<string>();
      const labels = Array.from(set);
      const hasOther = labels.includes(OTHER_SENTINEL);
      const notes = hasOther ? (otherText[q.question] ?? '').trim() : '';
      return {
        question: q.question,
        // Keep OTHER_SENTINEL in the wire payload — server strips it
        // before passing answers downstream. Lets the server distinguish
        // "user picked Other" from "user picked nothing".
        labels,
        notes,
      };
    });
    onDecide({ answers });
  };

  return (
    <Card className="my-2 border-warning" data-ask-user-card="pending">
      <CardHeader className="py-2">
        <CardTitle className="flex items-center gap-1 text-sm">
          <HelpCircle className="h-3.5 w-3.5" />
          <span>{t('workspaces:askUser.card.title')}</span>
          <span className="ml-auto text-xs text-warm-text-muted">
            {formatRemaining(remaining, t)}
          </span>
        </CardTitle>
      </CardHeader>
      <CardContent className="space-y-4 py-2">
        {request.questions.map((q, qi) => {
          const focusedKey = previewKey[q.question];
          const focusedOpt = q.options.find((o) => o.label === focusedKey);
          const focusedPreview = focusedOpt?.preview;
          return (
            <div key={`q-${qi}`} className="space-y-1">
              {q.header && (
                <div className="text-[10px] font-semibold uppercase tracking-wide text-warm-text-muted">
                  {q.header}
                </div>
              )}
              <div className="text-sm font-medium">{q.question}</div>
              <div className="space-y-1">
                {q.options.map((opt, oi) => {
                  const checked = selections[q.question]?.has(opt.label) ?? false;
                  const inputType = q.multiSelect ? 'checkbox' : 'radio';
                  const recommended = !!opt.recommended;
                  return (
                    <label
                      key={`q-${qi}-o-${oi}`}
                      className={
                        'flex items-start gap-2 rounded-md p-1.5 text-sm hover:bg-warm-surface-elevated cursor-pointer ' +
                        (recommended ? 'ring-1 ring-warning/40' : '')
                      }
                    >
                      <input
                        type={inputType}
                        name={`q-${request.id}-${qi}`}
                        checked={checked}
                        onChange={() => toggle(q.question, opt.label, q.multiSelect)}
                        className="mt-0.5"
                      />
                      <span className="flex-1">
                        <span className="font-medium inline-flex items-center gap-1">
                          {recommended && (
                            <Star className="h-3 w-3 fill-warning text-warning" />
                          )}
                          {opt.label}
                        </span>
                        {opt.description && (
                          <span className="block text-xs text-warm-text-muted">
                            {opt.description}
                          </span>
                        )}
                      </span>
                    </label>
                  );
                })}
                {/* Implicit "Other" — every question gets free-text fallback,
                    matching the official AskUserQuestion contract. */}
                <label
                  key={`q-${qi}-other`}
                  className="flex items-start gap-2 rounded-md p-1.5 text-sm hover:bg-warm-surface-elevated cursor-pointer"
                >
                  <input
                    type={q.multiSelect ? 'checkbox' : 'radio'}
                    name={`q-${request.id}-${qi}`}
                    checked={selections[q.question]?.has(OTHER_SENTINEL) ?? false}
                    onChange={() => toggle(q.question, OTHER_SENTINEL, q.multiSelect)}
                  />
                  <span className="flex-1 space-y-1">
                    <span className="font-medium">
                      {t('workspaces:askUser.card.other')}
                    </span>
                    {(selections[q.question]?.has(OTHER_SENTINEL) ?? false) && (
                      <textarea
                        className="w-full rounded-md border border-input bg-background px-2 py-1 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-1 focus-visible:ring-ring"
                        rows={2}
                        placeholder={t('workspaces:askUser.card.otherPlaceholder')}
                        value={otherText[q.question] ?? ''}
                        onChange={(e) =>
                          setOtherText((prev) => ({
                            ...prev,
                            [q.question]: e.target.value,
                          }))
                        }
                      />
                    )}
                  </span>
                </label>
              </div>
              {focusedPreview && (
                <pre className="mt-1 rounded-md border border-warm-border bg-warm-surface-elevated px-2 py-1.5 text-xs overflow-x-auto whitespace-pre-wrap">
                  {focusedPreview}
                </pre>
              )}
            </div>
          );
        })}
        <div className="flex justify-end">
          <Button size="sm" disabled={!allAnswered} onClick={handleSubmit}>
            {t('workspaces:askUser.card.submit')}
          </Button>
        </div>
      </CardContent>
    </Card>
  );
}

function msUntil(unixMs: number): number {
  return unixMs - Date.now();
}

function formatRemaining(ms: number, t: TFunction): string {
  if (ms <= 0) return t('workspaces:askUser.card.expired');
  const totalSec = Math.floor(ms / 1000);
  const m = Math.floor(totalSec / 60);
  const s = totalSec % 60;
  return t('workspaces:askUser.card.remaining', { m, s });
}

function labelForStatus(
  status: AskUserRequest['status'],
  t: TFunction,
): string {
  switch (status) {
    case 'answered':
      return t('workspaces:askUser.card.answered');
    case 'timeout':
      return t('workspaces:askUser.card.timeout');
    case 'cancelled':
      return t('workspaces:askUser.card.cancelled');
    default:
      return status;
  }
}
