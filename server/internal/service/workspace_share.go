package service

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/niuniu-dev/niuniu/internal/store"
)

// 工作空间分享（团队版，license.FeatureWorkspaceShare）。一个分享 = 指向某
// 工作空间聊天会话的只读外链 /share/ws/:token，必须带时效（expires_at）和
// 密码。安全模型：
//
//   - URL token：32 字节 CSPRNG、URL-safe base64；库里只存 sha256 hex。
//     token 即地址，不可枚举；泄露可 revoke。
//   - 密码：bcrypt 哈希存储；解锁成功后发 HMAC 签名的短时 cookie，后续读
//     请求凭 cookie，无需重复输密码。
//   - 时效：所有公开端点统一检查 expires_at + revoked_at，过期/撤销一律
//     ErrShareUnavailable（404，不区分原因，避免枚举）。
//
// 分享域名（share.base_url）是 admin 级 server setting：创建分享时用它拼出
// 对外的完整链接；未配置则退回相对路径，由前端补 origin。
type WorkspaceShareService struct {
	q        *store.Queries
	settings *ServerSettingsService
	secret   []byte
	now      func() time.Time // test seam
}

func NewWorkspaceShareService(q *store.Queries, settings *ServerSettingsService, secret []byte) *WorkspaceShareService {
	return &WorkspaceShareService{q: q, settings: settings, secret: secret, now: time.Now}
}

// SetNowFunc is exposed for tests: overrides the service clock.
func (s *WorkspaceShareService) SetNowFunc(f func() time.Time) {
	s.now = f
}

// KeyShareBaseURL 是 admin 设置键：分享链接的访问域名（如 https://share.example.com）。
// 空值 = 未配置，链接退回相对路径。
const KeyShareBaseURL = "share.base_url"

// ErrShareUnavailable 是所有公开访问路径的统一失败态：token 不存在、已撤销、
// 已过期。三种情况同错，避免枚举。
var ErrShareUnavailable = errors.New("workspace_share: unavailable")

// ErrSharePassword 是密码错误（与 ErrShareUnavailable 分开，便于返回 401
// 并让前端继续展示密码输入）。
var ErrSharePassword = errors.New("workspace_share: wrong password")

// ShareConstraints 是创建侧的硬约束。分享必须有时效（MaxLifetime 封顶），
// 密码有最低长度（防一字符密码）。
const (
	MinPasswordLen    = 4
	MaxLifetime       = 30 * 24 * time.Hour
	MinLifetime       = time.Minute
	unlockCookieTTL   = time.Hour
	unlockSecretBytes = 32
	// MaxSharedChatLen 是公开对话单条消息的长度上限。
	MaxSharedChatLen = 4000
)

// CanChat 报告该分享是否允许观看者对话。
func (r ResolvedShare) CanChat() bool { return r.Share.AllowChat == 1 }

func boolToInt64(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// GenerateUnlockSecret 生成服务端 HMAC 密钥（server.go 启动时调用；持久化
// 与否则由调用方决定——personal 版进程内生成即可，重启后旧 cookie 失效，
// 观看者重新输一次密码）。
func GenerateUnlockSecret() ([]byte, error) {
	buf := make([]byte, unlockSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// ShareInfo 是 Create 的返回：库内行 + 仅此一次返回的 raw token + 对外链接。
type ShareInfo struct {
	Share store.WorkspaceShare
	// RawToken 只在创建时返回一次，/share/ws/<RawToken> 即访问地址。
	RawToken string
	// Path 是 /share/ws/<token>（不含域名）。URL 为空时前端用 origin+Path。
	Path string
	// URL 是配置了 share.base_url 时的完整外链；未配置时为空串。
	URL string
}

// Create 生成新分享。password 明文进、bcrypt 出；expiresAt 必须落在
// [now+MinLifetime, now+MaxLifetime]。allowChat 默认 false（只读），开启后
// 解锁的观看者可以向工作空间 agent 发消息。
func (s *WorkspaceShareService) Create(ctx context.Context, workspaceID, createdBy int64, password string, expiresAt time.Time, allowChat bool) (ShareInfo, error) {
	if len(password) < MinPasswordLen {
		return ShareInfo{}, fmt.Errorf("password must be at least %d characters", MinPasswordLen)
	}
	now := s.now().UTC()
	expiresAt = expiresAt.UTC()
	if expiresAt.Before(now.Add(MinLifetime)) || expiresAt.After(now.Add(MaxLifetime)) {
		return ShareInfo{}, fmt.Errorf("expires_at must be between %s and %s from now", MinLifetime, MaxLifetime)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return ShareInfo{}, err
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return ShareInfo{}, err
	}
	rawToken := base64RawURL(buf)
	tokenHash := sha256Hex([]byte(rawToken))

	share, err := s.q.CreateWorkspaceShare(ctx, store.CreateWorkspaceShareParams{
		WorkspaceID:  workspaceID,
		TokenHash:    tokenHash,
		PasswordHash: string(hash),
		ExpiresAt:    expiresAt,
		CreatedBy:    createdBy,
		AllowChat:    boolToInt64(allowChat),
	})
	if err != nil {
		return ShareInfo{}, err
	}
	return ShareInfo{
		Share:    share,
		RawToken: rawToken,
		Path:     SharePath(rawToken),
		URL:      s.buildURL(ctx, rawToken),
	}, nil
}

// List 返回工作空间的分享列表（含最近过期未清理的），并顺带做一次惰性
// 清理：把已过期的行物理删掉（含密码哈希——过期即清除，不保留任何分享
// 凭据残留），避免表无限增长。
func (s *WorkspaceShareService) List(ctx context.Context, workspaceID int64) ([]store.WorkspaceShare, error) {
	s.SweepExpired(ctx)
	return s.q.ListWorkspaceSharesByWorkspace(ctx, workspaceID)
}

// SweepExpired 物理删除所有已过期的分享行。全表一刀切（按 expires_at 截止），
// 与具体工作空间无关——List 与公开访问路径都会顺手触发，保证过期行不残留。
func (s *WorkspaceShareService) SweepExpired(ctx context.Context) {
	if err := s.q.DeleteExpiredWorkspaceShares(ctx, s.now().UTC()); err != nil {
		// 清理失败不影响主流程可用性。
		_ = err
	}
}

// SetAllowChat 切换分享的是否可对话标记。
func (s *WorkspaceShareService) SetAllowChat(ctx context.Context, workspaceID, shareID int64, allowChat bool) error {
	return s.q.UpdateWorkspaceShareAllowChat(ctx, store.UpdateWorkspaceShareAllowChatParams{
		AllowChat:   boolToInt64(allowChat),
		ID:          shareID,
		WorkspaceID: workspaceID,
	})
}

// Revoke 撤销（软删）。目标不存在/已撤销时幂等成功——撤销语义只要求之后
// 不可访问。
func (s *WorkspaceShareService) Revoke(ctx context.Context, workspaceID, shareID int64) error {
	return s.q.RevokeWorkspaceShare(ctx, store.RevokeWorkspaceShareParams{
		ID:          shareID,
		WorkspaceID: workspaceID,
	})
}

// ResolvedShare 是 ResolveActive 的结果：分享行 + 工作空间展示信息。
// WorkspacePath 仅供服务端转发消息（Deliver）使用——公开响应体由 handler
// 手工构建，绝不直接序列化本结构。
type ResolvedShare struct {
	Share             store.WorkspaceShare
	WorkspaceID       int64
	WorkspaceName     string
	WorkspacePath     string
	WorkspaceArchived bool
	IssueTitle        string // 工作空间绑定 issue 的标题；未绑定为空
}

// ResolveActive 校验 token 对应的分享处于可访问状态（存在、未撤销、未过期），
// 并带出工作空间展示信息。任何不可访问状态都返回 ErrShareUnavailable。
func (s *WorkspaceShareService) ResolveActive(ctx context.Context, rawToken string) (ResolvedShare, error) {
	if rawToken == "" {
		return ResolvedShare{}, ErrShareUnavailable
	}
	share, err := s.q.GetWorkspaceShareByTokenHash(ctx, sha256Hex([]byte(rawToken)))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ResolvedShare{}, ErrShareUnavailable
		}
		return ResolvedShare{}, err
	}
	if share.RevokedAt.Valid || !s.now().UTC().Before(share.ExpiresAt) {
		// 已过期：物理删除（含密码哈希）。顺手全表清一遍，让无人再访问的
		// 过期行也在这次触达时清掉。撤销行保留（列表可展示已撤销状态）。
		if !s.now().UTC().Before(share.ExpiresAt) {
			s.SweepExpired(ctx)
		}
		return ResolvedShare{}, ErrShareUnavailable
	}
	ws, err := s.q.GetWorkspace(ctx, share.WorkspaceID)
	if err != nil {
		return ResolvedShare{}, ErrShareUnavailable
	}
	resolved := ResolvedShare{
		Share:             share,
		WorkspaceID:       ws.ID,
		WorkspaceName:     ws.Name,
		WorkspacePath:     ws.Path,
		WorkspaceArchived: ws.IsArchived == 1,
	}
	// 工作空间 1:1 绑定 issue 时带出标题，给分享页做上下文头。
	if ws.IssueID.Valid && ws.IssueID.Int64 > 0 {
		if issue, ierr := s.q.GetIssue(ctx, ws.IssueID.Int64); ierr == nil {
			resolved.IssueTitle = issue.Title
		}
	}
	return resolved, nil
}

// VerifyPassword 校验访问密码；成功后触碰 last_access_at（主人在列表里能
// 看到最近访问迹象）。失败返回 ErrSharePassword。
func (s *WorkspaceShareService) VerifyPassword(ctx context.Context, share store.WorkspaceShare, password string) error {
	if bcrypt.CompareHashAndPassword([]byte(share.PasswordHash), []byte(password)) != nil {
		return ErrSharePassword
	}
	_ = s.q.TouchWorkspaceShareAccess(ctx, share.ID)
	return nil
}

// SignUnlock 签发解锁 cookie 值：shareID|tokenHash前8字节|expUnix|hmac。
// 绑定 tokenHash 前缀，防止 A 分享的 cookie 拿去 B 分享用。
func (s *WorkspaceShareService) SignUnlock(shareID int64, tokenHash string) string {
	exp := s.now().UTC().Add(unlockCookieTTL).Unix()
	payload := unlockPayload(shareID, tokenHash, exp)
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(payload))
	return fmt.Sprintf("%d|%x|%x", shareID, exp, mac.Sum(nil))
}

// VerifyUnlock 校验解锁 cookie 是否对应该 tokenHash 的分享且未过期。
func (s *WorkspaceShareService) VerifyUnlock(shareID int64, tokenHash, value string) bool {
	parts := strings.Split(value, "|")
	if len(parts) != 3 {
		return false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id != shareID {
		return false
	}
	var exp int64
	if _, err := fmt.Sscanf(parts[1], "%x", &exp); err != nil {
		return false
	}
	if s.now().UTC().Unix() >= exp {
		return false
	}
	mac := hmac.New(sha256.New, s.secret)
	mac.Write([]byte(unlockPayload(shareID, tokenHash, exp)))
	return hmac.Equal([]byte(parts[2]), []byte(hex.EncodeToString(mac.Sum(nil))))
}

// buildURL 用 share.base_url 拼完整外链；未配置返回空串。
func (s *WorkspaceShareService) buildURL(ctx context.Context, rawToken string) string {
	base := strings.TrimRight(s.settings.GetString(ctx, KeyShareBaseURL, ""), "/")
	if base == "" {
		return ""
	}
	return base + SharePath(rawToken)
}

// SharePath 是公开访问路径（不含域名）。
func SharePath(rawToken string) string {
	return "/share/ws/" + rawToken
}

func unlockPayload(shareID int64, tokenHash string, expUnix int64) string {
	return fmt.Sprintf("%d|%s|%d", shareID, tokenHash[:8], expUnix)
}

func base64RawURL(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}
