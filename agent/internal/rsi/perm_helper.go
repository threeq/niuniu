package rsi

import "github.com/niuniu-dev/niuniu/agent/internal/perm"

// allPerms is a Checker that approves everything (explore runs in a
// throwaway sandbox with -y semantics).
type allPerms struct{}

func (allPerms) Check(string) perm.Decision { return perm.Allow }

func newAllPerms() perm.Checker { return allPerms{} }
