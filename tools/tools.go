//go:build tools

// Package tools pins the Phase 0 dependency set so go.mod/go.sum retain
// the exact versions from docs/implementation-plan.md until Phase 1 streams
// import them for real. Never imported by normal builds (tools tag only);
// see docs/tech-stack.md for why each dependency exists.
package tools

import (
	_ "github.com/alecthomas/participle/v2"
	_ "github.com/coreos/go-oidc/v3/oidc"
	_ "github.com/expr-lang/expr"
	_ "github.com/fsnotify/fsnotify"
	_ "github.com/yuin/goldmark/v2"
	_ "golang.org/x/crypto/argon2"
	_ "golang.org/x/oauth2"
	_ "gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)
