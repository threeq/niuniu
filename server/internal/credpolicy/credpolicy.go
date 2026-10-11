// Package credpolicy holds the canonical credential-handling discipline text
// shared by every surface that walks a user through credential setup: built-in
// skill prompts, MCP tool descriptions, and onboarding kickoff prompts.
//
// It is a dependency-free leaf package so the MCP binary (which deliberately
// imports only light internal packages) can reference the same wording as the
// server without pulling in the service layer. Keeping one copy means the rule
// is stated identically everywhere and can be revised in one place.
package credpolicy

// Discipline is the one-line rule every credential-guidance surface must state
// verbatim (Chinese; the product's default UI language).
const Discipline = "安全铁律：绝不在对话里索取或接收明文密钥——凭据一律通过一次性安全链接提交，页面独立收取，内容不进入对话。"

// DisciplineEN is the English mirror of Discipline.
const DisciplineEN = "Security rule: never ask for or accept plaintext secrets in the conversation. " +
	"Credentials are submitted only through a one-time secure link, on a separate page, and never enter the conversation."

// LinkNote is the note returned alongside a freshly issued credential link. It
// tells the agent exactly what to do with the link and re-states the rule.
const LinkNote = "url 是站点内相对路径，浏览器会按当前页面自动补全协议与域名/端口（个人版与团队版通用）。" +
	"请把 link_markdown 原样发给用户（会渲染成可点击链接）；" + Discipline
