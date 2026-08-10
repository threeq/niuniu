import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Check, ChevronRight, ChevronDown } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import { remarkGfmSafariSafe } from '@/lib/remark-gfm-safari-safe'
import { rehypeLinkify } from '@/lib/rehype-linkify'

const REMARK_PLUGINS = [remarkGfmSafariSafe()]
const REHYPE_PLUGINS = [rehypeLinkify()]

interface IssueDescriptionProps {
  description: string
  onSave: (description: string) => void
  readOnly?: boolean
}

export function IssueDescription({ description, onSave, readOnly }: IssueDescriptionProps) {
  const { t } = useTranslation('projects')
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(description)
  const [copied, setCopied] = useState(false)
  // #623 review: description is collapsible and collapsed by default to keep the
  // panel compact (same behaviour in both the kanban and workspace issue panels).
  const [collapsed, setCollapsed] = useState(true)

  const handleEdit = () => {
    setDraft(description)
    setCollapsed(false)
    setEditing(true)
  }

  const handleSave = () => {
    onSave(draft)
    setEditing(false)
  }

  const handleCancel = () => {
    setDraft(description)
    setEditing(false)
  }

  const handleCopy = () => {
    if (!description) return
    navigator.clipboard.writeText(description)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  const Chevron = collapsed ? ChevronRight : ChevronDown

  return (
    <div className="border-b border-border px-5 py-4">
      <div className="flex justify-between items-center mb-2">
        <button
          type="button"
          className="flex items-center gap-1.5 text-left hover:text-foreground"
          onClick={() => setCollapsed((c) => !c)}
          aria-expanded={!collapsed}
        >
          <Chevron className="w-3.5 h-3.5 text-muted-foreground" />
          <span className="text-xs font-semibold text-muted-foreground">{t('issue.description.label')}</span>
          {copied && <Check className="w-3 h-3 text-green-500" />}
        </button>
        {!editing && !readOnly && !collapsed && (
          <button className="text-xs text-primary hover:underline" onClick={handleEdit}>
            {t('issue.description.edit')}
          </button>
        )}
      </div>

      {collapsed ? null : editing ? (
        <div>
          <textarea
            className="w-full bg-background border border-border rounded-md p-3 text-sm min-h-[120px] resize-y focus:outline-none focus:ring-1 focus:ring-primary"
            value={draft}
            onChange={(e) => setDraft(e.target.value)}
            placeholder={t('issue.description.placeholder')}
            autoFocus
          />
          <div className="flex justify-end gap-2 mt-2">
            <button className="text-xs px-3 py-1 border border-border rounded hover:bg-accent" onClick={handleCancel}>{t('issue.description.cancel')}</button>
            <button className="text-xs px-3 py-1 bg-primary text-primary-foreground rounded hover:opacity-90" onClick={handleSave}>{t('issue.description.save')}</button>
          </div>
        </div>
      ) : (
        <div
          className="prose prose-sm dark:prose-invert max-w-none text-sm cursor-pointer hover:bg-accent/30 rounded p-1 -m-1 min-h-[2rem]"
          onClick={handleCopy}
          title={description ? t('issue.description.copyHint') : undefined}
        >
          {description ? (
            <ReactMarkdown
              remarkPlugins={REMARK_PLUGINS}
              rehypePlugins={REHYPE_PLUGINS}
            >
              {description}
            </ReactMarkdown>
          ) : (
            <span className="text-muted-foreground italic">{t('issue.description.empty')}</span>
          )}
        </div>
      )}
    </div>
  )
}
