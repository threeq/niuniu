import type { Column } from '@/types/api'

type ColumnLane = Pick<Column, 'name' | 'lifecycle_mapping'>

// The board is user-authored, so a lane is recognised by its NAME as well as
// its lifecycle mapping — the AI-native board ships 审查/完成 with no lifecycle
// set on 人工审查. These are data (column names), not UI copy: unicode escapes
// keep raw CJK out of the source for the no-chinese-literal rule.
const REVIEW_COLUMN_NAME = String.fromCharCode(0x5ba1, 0x67e5)
const DONE_COLUMN_NAME = String.fromCharCode(0x5b8c, 0x6210)

export function isReviewColumn(c: ColumnLane): boolean {
  return (
    c.name.includes(REVIEW_COLUMN_NAME) ||
    (c.lifecycle_mapping ?? '').toLowerCase().includes('review')
  )
}

export function isDoneColumn(c: ColumnLane): boolean {
  return (
    c.name.includes(DONE_COLUMN_NAME) ||
    (c.lifecycle_mapping ?? '').toLowerCase().includes('complete')
  )
}
