package license

import "errors"

// License error sentinels. 定义在 core 契约层：开源 HTTP 层（api/license.go、
// license_middleware.go）和企业实现共享同一错误身份（errors.Is 跨包有效）。
// 真实 Install/Verify 逻辑在企业包 internal/enterprise/license，返回这些哨兵。
var (
	ErrSeatExceeded   = errors.New("license: seat limit reached")
	ErrExpired        = errors.New("license: already expired")
	ErrInvalidContent = errors.New("license: invalid content")
	ErrMalformed      = errors.New("license: malformed file")
	ErrSignature      = errors.New("license: signature verification failed")
	ErrNoKeys         = errors.New("license: no public keys configured")
)
