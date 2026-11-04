package githubsource

// Outbound half of the GitHub Source (Epic #755 / design §6.10): write a
// finished task back into the PR/issue thread it came from.
//
// Egress is NOT decided here. The caller (the service layer) resolves the
// thread, checks the owner's write grant and loads the repo credential; this
// file only performs the API call.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"
)

// MaxReplyLen bounds a posted comment. GitHub's hard limit is 65536 bytes; keep
// headroom for the truncation marker.
const MaxReplyLen = 60000

// ReplyConfig is one outbound comment.
type ReplyConfig struct {
	Repo   string // owner/name
	Number string // PR or issue number
	Token  string
	Body   string
}

// Comment is the created comment.
type Comment struct {
	ID  int64  `json:"id"`
	URL string `json:"html_url"`
}

// PostComment posts a comment to an issue or pull request thread. The same
// endpoint serves both: a PR is an issue on GitHub's comments API.
func PostComment(ctx context.Context, client HTTPClient, baseURL string, cfg ReplyConfig) (Comment, error) {
	if _, _, ok := splitRepo(cfg.Repo); !ok {
		return Comment{}, fmt.Errorf("githubsource: invalid repo %q", cfg.Repo)
	}
	number, err := strconv.Atoi(strings.TrimSpace(cfg.Number))
	if err != nil || number <= 0 {
		return Comment{}, fmt.Errorf("githubsource: invalid thread number %q", cfg.Number)
	}
	body := truncateReply(strings.TrimSpace(cfg.Body))
	if body == "" {
		return Comment{}, fmt.Errorf("githubsource: empty reply")
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return Comment{}, fmt.Errorf("githubsource: reply needs a credential")
	}

	payload, err := json.Marshal(map[string]string{"body": body})
	if err != nil {
		return Comment{}, err
	}
	target := fmt.Sprintf("%s/repos/%s/issues/%d/comments", baseURLOf(baseURL), repoPath(cfg.Repo), number)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(payload))
	if err != nil {
		return Comment{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "niuniu-github-source")
	req.Header.Set("Authorization", "Bearer "+cfg.Token)

	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return Comment{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Comment{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &apiErr)
		return Comment{}, &APIError{Status: resp.StatusCode, Message: apiErr.Message}
	}
	var out Comment
	if err := json.Unmarshal(raw, &out); err != nil {
		return Comment{}, fmt.Errorf("githubsource: decode comment: %w", err)
	}
	return out, nil
}

func baseURLOf(base string) string {
	if b := strings.TrimRight(strings.TrimSpace(base), "/"); b != "" {
		return b
	}
	return DefaultBaseURL
}

// truncateReply caps the body at a rune boundary so a multi-byte answer is
// never split mid-character. GitHub's limit is bytes, so the cut is measured
// in bytes, not runes.
func truncateReply(body string) string {
	if len(body) <= MaxReplyLen {
		return body
	}
	cut := 0
	for _, r := range body {
		n := utf8.RuneLen(r)
		if cut+n > MaxReplyLen {
			break
		}
		cut += n
	}
	return body[:cut] + "\n\n…（回复过长，已截断）"
}
