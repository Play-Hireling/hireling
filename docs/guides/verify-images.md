# Verify a published image

Release images are published by [`.github/workflows/release.yml`](../../.github/workflows/release.yml) as a **manifest list** for `linux/amd64` and `linux/arm64`, with SLSA provenance, an SBOM, and a **keyless cosign** signature over the digest. No signing key exists: the identity comes from the GitHub Actions OIDC token of that one workflow file.

This page is what you run to check an image before deploying it.

## What is published

| Image | Tag |
| --- | --- |
| `ghcr.io/<owner>/hireling-runtime` | the git tag (`v0.1.0`), plus `sha-<short>` from the same commit |
| `ghcr.io/<owner>/hireling-tavern-core` | same |
| `ghcr.io/<owner>/hireling-egress` | same |
| `ghcr.io/<owner>/hireling-ledger-writer` | same |
| `ghcr.io/<owner>/hireling-gbrain` | same |

There is no `latest`: the workflow sets `latest=false` on purpose, and `sha-<short>` is the only tag a manual run from a branch produces — that is how the multi-arch manifest and the signature are tested without cutting a release.

A tag can be moved to another image. **Deploy by digest**, not by tag: `ghcr.io/<owner>/hireling-tavern-core@sha256:…`.

## Both architectures are in the manifest

```bash
docker buildx imagetools inspect ghcr.io/<owner>/hireling-tavern-core:v0.1.0
```

Expect the header to report a digest for the *list*, and a manifest section per platform:

```text
Name:      ghcr.io/<owner>/hireling-tavern-core:v0.1.0
MediaType: application/vnd.oci.image.index.v1+json
Digest:    sha256:1111…            <- the list; this is what you pin

Manifests:
  Name:        ghcr.io/<owner>/hireling-tavern-core:v0.1.0@sha256:aaaa…
  Platform:    linux/amd64
  …

  Name:        ghcr.io/<owner>/hireling-tavern-core:v0.1.0@sha256:bbbb…
  Platform:    linux/arm64
  …

  Name:        ghcr.io/<owner>/hireling-tavern-core:v0.1.0@sha256:cccc…
  Platform:    unknown/unknown      <- provenance
  …

  Name:        ghcr.io/<owner>/hireling-tavern-core:v0.1.0@sha256:dddd…
  Platform:    unknown/unknown      <- SBOM
  …
```

`unknown/unknown` entries are the attestations, not a third architecture. Two `linux/*` platforms is what "multi-arch" means here; `docker pull` on an Apple Silicon laptop and on a plain amd64 VPS then fetches the same tag.

Only the `last` shows an example output shape — check the real run, not these placeholders.

## The signature

Install cosign (v3):

```bash
cosign version            # v3.1.4 is what the workflow uses
```

Verify a digest (or a tag — cosign resolves it to the digest and says which one it verified):

```bash
cosign verify ghcr.io/<owner>/hireling-tavern-core@sha256:1111… \
  --certificate-identity-regexp '^https://github\.com/Play-Hireling/hireling/\.github/workflows/release\.yml@refs/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

Success looks like a JSON array of signatures with a `critical.identity.docker-reference` equal to the image you asked about, and the certificate subject set to the workflow ref:

```text
Verification for ghcr.io/<owner>/hireling-tavern-core@sha256:1111… --
The following checks were performed on each of these signatures:
  - The cosign claims were validated
  - Existence of the claims in the transparency log was verified offline
  - The code-signing certificate was verified using trusted certificate authority certificates
[{"critical":{"identity":{"docker-reference":"ghcr.io/<owner>/hireling-tavern-core"},"image":{"docker-manifest-digest":"sha256:1111…"},"type":"cosign container image signature"},"optional":{…}}]
```

Pin the identity to the tag you trust instead of the regexp above when you want a stricter check:

```bash
--certificate-identity 'https://github.com/Play-Hireling/hireling/.github/workflows/release.yml@refs/tags/v0.1.0'
```

A `sha-<short>` image built by a manual run from a branch has the same identity with `@refs/heads/<branch>` — verification still passes, and the certificate tells you which branch and commit produced it.

## Provenance and SBOM

Both are attached to the same index, so they can be checked against the same digest:

```bash
cosign verify-attestation ghcr.io/<owner>/hireling-tavern-core@sha256:1111… \
  --type slsaprovenance \
  --certificate-identity-regexp '^https://github\.com/Play-Hireling/hireling/\.github/workflows/release\.yml@refs/' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com
```

```bash
cosign download attestation ghcr.io/<owner>/hireling-tavern-core@sha256:1111… \
  | jq -r '.payload' | base64 -d | jq '.predicate'
```

The provenance names the workflow file, the repository and the commit that produced the image — that is the reason the signature is worth something.

## Signing your own local build

`docker compose up -d --build` produces `hireling/<name>:<HIRELING_TAG>` on your machine. Those images are **not** signed and not covered by any of this; verify only what came from GHCR.

## If verification fails

| Symptom | Cause |
| --- | --- |
| `no signatures found` | the digest has no `.sig` tag: the image was published before signing existed, or the publish job did not reach the cosign step |
| `none of the expected identities matched` | someone else's build, or a fork of the workflow; check `--certificate-identity` against the URL you expect |
| `MANIFEST_UNKNOWN` / `unauthorized` | the GHCR package is still private; log in with a token that can read packages, and make the package public if downloaders must verify it |
| `Platform: unknown/unknown` on `inspect` | that is the attestation, not an error |

Do not deploy an image whose digest you could not verify.
