# Use of vendored code in Weave Scope

Weave Scope is licensed under the [Apache 2.0 license](LICENSE).

Some dependencies are under different licenses though.

Note: this repo built with a committed `vendor/` directory (Go's `-mod
vendor` mode) through 2026-08, which physically bundled dependency source
and license text under `./vendor/`. As of Phase 2 of
`MODERNIZATION-PLAN.md`, `vendor/` was dropped in favor of building
straight from `go.sum`/the module cache — the paths below no longer exist
in this repo. The pinned version of each dependency (and its license
text) is still verifiable via `go.sum` and fetchable from the upstream
repository at that exact version; nothing here changes what's actually
linked into the built binary, only where its source physically lives.

- Under MPL-2.0, still pinned in `go.mod`:
  - https://github.com/weaveworks/go-checkpoint
  - https://github.com/hashicorp/go-cleanhttp
  - https://github.com/certifi/gocertifi
  - https://github.com/hashicorp/golang-lru (pulled in transitively)

- The docs of a dependency that's pulled in transitively are under
  CC-BY 4.0: https://github.com/docker/go-units (still pinned in `go.mod`)

- No longer dependencies (stale even before the `vendor/` removal, so
  removed here too): `hashicorp/go-version` and `howeyc/gopass`
  (the CDDL-licensed `terminal_solaris.go` this used to flag) aren't in
  `go.mod` at all anymore.

[One file used in tests](COPYING.LGPL-3) is under LGPL-3, that's why we ship
the license text in this repository.
