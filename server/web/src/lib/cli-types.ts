// Single source of truth for the agent engine (`cli_type`) enum.
//
// Every engine the backend accepts must be listed here exactly once. The
// `CliType` union is *derived* from the array rather than written alongside
// it, so a new engine can never be added to the type but forgotten in the
// pickers (that drift is how `goose` shipped missing from the project
// settings dropdown).
//
// This module intentionally imports nothing — `@/types/api` re-exports
// `CliType` from here, so any import in the other direction would cycle.

// Display order for every engine picker. Not alphabetical and not the
// backend's order: it front-loads the engines most users reach for, with the
// protocol-backed ones trailing. Append new engines at the end.
export const CLI_TYPES = ['claude', 'codex', 'qwen', 'cursor', 'goose', 'omp'] as const

export type CliType = (typeof CLI_TYPES)[number]

export const DEFAULT_CLI_TYPE: CliType = 'claude'

// Narrows an arbitrary string (API payload, <select> value, persisted draft)
// to a known engine.
export function isCliType(value: string): value is CliType {
  return (CLI_TYPES as readonly string[]).includes(value)
}

// Arrow-key handling for the engine radio groups. Returns the engine to move
// to, or `null` when the key is not a navigation key so the caller can leave
// the event alone.
//
// Wraps around at both ends, matching the native radio group behaviour the
// `role="radiogroup"` markup promises. Both axes are accepted because the
// pickers wrap onto multiple rows — Up/Down there is as natural as Left/Right,
// and treating them as one linear sequence keeps every option reachable
// regardless of how many columns the current breakpoint renders.
export function cliTypeForArrowKey(current: CliType, key: string): CliType | null {
  const i = CLI_TYPES.indexOf(current)
  if (i < 0) return null
  if (key === 'ArrowRight' || key === 'ArrowDown') {
    return CLI_TYPES[(i + 1) % CLI_TYPES.length]
  }
  if (key === 'ArrowLeft' || key === 'ArrowUp') {
    return CLI_TYPES[(i - 1 + CLI_TYPES.length) % CLI_TYPES.length]
  }
  return null
}
