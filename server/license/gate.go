package license

import "context"

// Gate 是核心装配依赖的"许可证契约"（接缝）。
//
// 开源 core 用 NopGate（始终放行、永不只读、席位无限）；企业构建注入真实的
// 席位/license Service。这个接口让商业 license 逻辑可以搬到开源 core 之外
// （enterprise 包），而 core 只依赖契约——这也是把开源 core 做成独立
// submodule 的前提（私有代码不再需要 core 的 internal license 实现）。
type Gate interface {
	AllowRun() bool
	ReadOnly() bool
	CheckSeatAvailable(ctx context.Context) error
	Install(ctx context.Context, blob string) error
	Load(ctx context.Context) error
	StartTicker(ctx context.Context)
	Status(ctx context.Context) Status
	SetEnforced(v bool)
	SetSeatCounter(f func(context.Context) (int64, error))
}

// NopGate 是 no-op Gate：始终允许运行、非只读、席位充足、Install 无操作。
// 这是开源个人版的默认实现（个人版不执行席位/许可证强制）。
type NopGate struct{}

func (NopGate) AllowRun() bool                                      { return true }
func (NopGate) ReadOnly() bool                                      { return false }
func (NopGate) CheckSeatAvailable(context.Context) error            { return nil }
func (NopGate) Install(context.Context, string) error               { return nil }
func (NopGate) Load(context.Context) error                          { return nil }
func (NopGate) StartTicker(context.Context)                         {}
func (NopGate) Status(context.Context) Status                       { return Status{State: StateActive} }
func (NopGate) SetEnforced(bool)                                    {}
func (NopGate) SetSeatCounter(func(context.Context) (int64, error)) {}
