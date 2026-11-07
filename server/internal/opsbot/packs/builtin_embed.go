package packs

import "embed"

// BuiltinFS holds the built-in Pack seeds in full: manifests, sidecar schema
// files, prompts, browser recipes and sandbox fixtures. Installing from a
// manifest-only embed would ship browser packs without the recipes they bind,
// so InstallBuiltins must copy every file under each version directory.
//
//go:embed all:builtin
var BuiltinFS embed.FS
