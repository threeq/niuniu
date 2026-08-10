//go:build enterprise

package main

// 团队构建（go build -tags enterprise）通过本文件注入真实 license Gate：
// import 触发 internal/enterprise/license 的 init()，把 core 的 license.Factory
// 设为真实席位/license 实现。
//
// 开源/个人构建不含本文件（无 enterprise tag），故 core 的 Factory 保持 nil，
// server.go 走 NopGate（无席位强制）。这也让开源 core 在物理上没有 enterprise
// 目录时仍能编译——本文件被 build tag 排除，不解析该 import。
import _ "github.com/threeq/niuniu-enterprise/enterprise/license"
