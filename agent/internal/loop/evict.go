package loop

import (
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// DefaultEvictKeepBytes caps one evicted tool_result text.
const DefaultEvictKeepBytes = 1 << 10

// evictMark is appended to truncated results. Its presence makes eviction
// one-shot: marked results are never rewritten, so the prompt prefix stays
// byte-stable after the initial pass (prompt-cache friendly).
const evictMark = "… [result evicted: task completed — re-run the tool if you need the full output]"

// evictOldToolResults truncates tool_result texts outside the keep-window
// (the N most recent stay verbatim). One-shot per result; older marked
// results are skipped. Runs at the top of every round; on a quiet round it
// is a no-op scan.
func (s *Session) evictOldToolResults(keepRecent, keepBytes int) {
	if keepRecent <= 0 {
		return
	}
	if keepBytes <= 0 {
		keepBytes = DefaultEvictKeepBytes
	}
	type ref struct{ mi, bi int }
	var refs []ref
	for mi := range s.messages {
		blocks := s.messages[mi].Blocks
		for bi := range blocks {
			if blocks[bi].Type == model.BlockToolResult {
				refs = append(refs, ref{mi, bi})
			}
		}
	}
	if len(refs) <= keepRecent {
		return
	}
	for _, r := range refs[:len(refs)-keepRecent] {
		blk := &s.messages[r.mi].Blocks[r.bi]
		if blk.Evicted || strings.HasSuffix(blk.Text, evictMark) {
			continue
		}
		if len(blk.Text) <= keepBytes {
			continue
		}
		blk.Text = blk.Text[:keepBytes] + evictMark
		blk.Evicted = true
	}
}

var _ = fmt.Sprintf   // fmt used by mark composition below if extended
var _ = model.Block{} // keep model import for Block pointer arithmetic
