package license

import (
	"context"
	"database/sql"
)

// DBRunner 是 license Gate 需要的最小 DB 面（Exec + QueryRow）。定义在公开契约
// 里，让企业包（外部 module）依赖它而非 core 的 internal store。*store.DB 与
// *sql.DB 都满足；*store.DB 在运行时仍保留 PG 的 ?→$N 改写。
type DBRunner interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// FactoryOpts 传给 Factory 以构造真实 license Gate。
type FactoryOpts struct {
	DB   DBRunner
	Path string
}

// Factory 若被设置，返回真实（企业）license Gate。开源 core 留空（nil），用
// NopGate；企业包通过 init() 设置它（团队构建的 main import 企业包触发 init），
// 于是团队版获得真实席位/license 强制，而开源构建对 enterprise 零引用。
var Factory func(FactoryOpts) Gate
