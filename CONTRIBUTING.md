# Contributing to Hireling

Thanks for helping build the tavern. This page is everything you need for a first pull request.

## Ways to help

- **Hireling templates** — a new role in `agents/<id>/` (profile + prompt). Easiest way to start; see [docs/guides/add-a-hireling.md](docs/guides/add-a-hireling.md).
- **Docs** — anything that confused you is a bug in the docs.
- **Bugs and tests** — the runtime and tavern-core are plain Go with table tests.
- **Features** — check the [roadmap](docs/roadmap.md) and open an issue before a large change, so we agree on the shape first.

## Ground rules

- **English** for code, comments, commits, issues and docs.
- **Security issues never go in public issues** — see [SECURITY.md](SECURITY.md).
- **No real data in fixtures**: no customer, financial, KYC or legal data, no real tokens, no personal emails. Use `example.com`.
- Be kind. [CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) applies everywhere in the project.

## Set up

You need Go 1.24 and Docker (only for the full stack).

```bash
git clone https://github.com/Play-Hireling/hireling.git && cd hireling
make test          # go vet + go test in every module
make fmt           # gofmt
docker compose up -d --build   # the full tavern (see the quickstart)
```

There is no root `go.mod`: `runtime/`, `services/tavern-core/` and `services/egress/` are separate modules. tavern-core uses the runtime's `profile` package through a `replace` directive.

## Pull requests

1. Fork, branch from `main` (`feat/…`, `fix/…`, `docs/…`).
2. Keep the PR focused; one idea per PR.
3. Add or update tests. `make test` must pass.
4. Update the docs in the same PR when behaviour changes (`docs/`, the package comment, the profile reference).
5. Title in [Conventional Commits](https://www.conventionalcommits.org/) form: `feat(runtime): …`, `fix(tavern-core): …`, `docs: …`.
6. **Sign off every commit** (DCO, below).

Maintainers merge with **squash and merge**: the PR title and body become the commit on `main`.

## Developer Certificate of Origin

We use the [DCO](https://developercertificate.org/) instead of a CLA. By signing off you certify that you wrote the change or have the right to submit it under the project's licenses. Add the line with `git commit -s`:

```
Signed-off-by: Your Name <you@example.com>
```

A check on every PR verifies the sign-off. Forgot it? `git commit --amend -s` (or `git rebase --signoff main`) and push again.

Contributions land under the license of the directory they touch ([LICENSING.md](LICENSING.md)).

## Code style

- `gofmt`, `go vet`, small packages, errors wrapped with context, no panics in request paths.
- Comments say *why*. Feature ids (A5, B11, …) in comments point to [docs/reference/feature-ids.md](docs/reference/feature-ids.md).
- Anything that leaves a container goes through `runtime/internal/redact`.
- Never log a secret, a token or a response body that might carry one.

## Where to talk

- **Questions and ideas:** GitHub Discussions.
- **Bugs and concrete proposals:** GitHub Issues (templates provided).
