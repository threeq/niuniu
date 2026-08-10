import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Send, RotateCcw } from 'lucide-react'

interface IssueCommentInputProps {
  onSubmit: (data: { author: string; content: string }) => void
  isSubmitting?: boolean
  // Review 闭环 (#623): optional secondary action folded into the composer. When
  // provided, a second button ("需重做/打回") appears next to 评论 — same text box,
  // but instead of a plain comment it posts the feedback AND bounces the card back to
  // the implement lane (injecting the two-layer review context). Rendered only where
  // the parent allows it (issue in a review / done column).
  onRequestChanges?: (data: { author: string; content: string }) => void
  requestChangesLabel?: string
  isRequestingChanges?: boolean
}

export function IssueCommentInput({
  onSubmit,
  isSubmitting,
  onRequestChanges,
  requestChangesLabel,
  isRequestingChanges,
}: IssueCommentInputProps) {
  const { t } = useTranslation('projects')
  const [content, setContent] = useState('')
  const [author, setAuthor] = useState('')

  const handleSubmit = () => {
    if (!content.trim()) return
    onSubmit({ author: author.trim(), content: content.trim() })
    setContent('')
  }

  const handleRequestChanges = () => {
    if (!content.trim() || !onRequestChanges) return
    onRequestChanges({ author: author.trim(), content: content.trim() })
    setContent('')
  }

  const busy = isSubmitting || isRequestingChanges

  return (
    <div className="bg-accent/20 border border-border rounded-lg p-3">
      <input
        className="w-full bg-transparent text-xs text-muted-foreground placeholder:text-muted-foreground/50 mb-2 outline-none"
        placeholder={t('issue.comment.authorPlaceholder')}
        value={author}
        onChange={(e) => setAuthor(e.target.value)}
      />
      <textarea
        className="w-full bg-transparent text-sm placeholder:text-muted-foreground/50 resize-none outline-none min-h-[60px]"
        placeholder={t('issue.comment.contentPlaceholder')}
        value={content}
        onChange={(e) => setContent(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) handleSubmit()
        }}
      />
      <div className="flex justify-between items-center mt-2">
        <span className="text-[10px] text-muted-foreground">{t('issue.comment.ctrlEnterHint')}</span>
        <div className="flex items-center gap-2">
          {onRequestChanges && (
            <button
              className="flex items-center gap-1 border border-border text-warning px-3 py-1 rounded text-xs hover:bg-accent disabled:opacity-50"
              onClick={handleRequestChanges}
              disabled={!content.trim() || busy}
              title={requestChangesLabel}
            >
              <RotateCcw className="w-3 h-3" />
              {requestChangesLabel}
            </button>
          )}
          <button
            className="flex items-center gap-1 bg-primary text-primary-foreground px-3 py-1 rounded text-xs hover:opacity-90 disabled:opacity-50"
            onClick={handleSubmit}
            disabled={!content.trim() || busy}
          >
            <Send className="w-3 h-3" />
            {t('issue.comment.send')}
          </button>
        </div>
      </div>
    </div>
  )
}
