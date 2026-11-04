import { useTranslation } from 'react-i18next'

/** Relative time in the active UI language (2 分钟前 / 2 minutes ago). */
export function useRelativeTime() {
  const { i18n } = useTranslation()
  const rtf = new Intl.RelativeTimeFormat(i18n.language, { numeric: 'auto' })
  return (iso: string) => {
    const then = new Date(iso).getTime()
    if (Number.isNaN(then)) return ''
    const diffSeconds = Math.round((then - Date.now()) / 1000)
    const abs = Math.abs(diffSeconds)
    if (abs < 60) return rtf.format(diffSeconds, 'second')
    if (abs < 3600) return rtf.format(Math.round(diffSeconds / 60), 'minute')
    if (abs < 86400) return rtf.format(Math.round(diffSeconds / 3600), 'hour')
    return rtf.format(Math.round(diffSeconds / 86400), 'day')
  }
}
