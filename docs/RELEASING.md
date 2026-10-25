# Releasing

Releases are built by GoReleaser in GitHub Actions (`.github/workflows/release.yml`) when a `v*` tag is pushed. The
workflow signs the checksum file with the project's GPG key and creates a **draft** GitHub release; publishing the
draft makes it visible to the Terraform Registry, which ingests it for `registry.terraform.io/aurimasniekis/deed`.

Nothing has been published yet. The first release needs the one-time owner setup below.

## One-time owner prerequisites

1. **Signing key.** Create a dedicated GPG key for provider releases. The Terraform Registry accepts RSA and DSA keys
   only (not ECC):

   ```sh
   gpg --full-generate-key                      # RSA and RSA, 4096 bits, no expiry or a long one; set a passphrase
   gpg --list-secret-keys --keyid-format=long   # note the fingerprint
   gpg --armor --export <FINGERPRINT>            # public key, for the Registry
   gpg --armor --export-secret-keys <FINGERPRINT> # private key, for the GitHub secret
   ```

   Keep an offline backup of the private key and its revocation certificate.

2. **Repository secrets** (GitHub → `aurimasniekis/terraform-provider-deed` → Settings → Secrets and variables →
   Actions):

   | Secret | Value |
   |---|---|
   | `GPG_PRIVATE_KEY` | the ASCII-armored private key |
   | `PASSPHRASE` | its passphrase |

   `GITHUB_TOKEN` is provided by Actions; the workflow needs `contents: write` (already declared).

3. **Registry namespace.** Sign in to <https://registry.terraform.io> with the GitHub account `aurimasniekis`, add the
   public key under *User Settings → Signing Keys* for the namespace `aurimasniekis`, then *Publish → Provider* and
   select the public repository `aurimasniekis/terraform-provider-deed`. The Registry requires the repository name
   `terraform-provider-<type>`, which gives the source address `aurimasniekis/deed`, and the release assets produced
   by `.goreleaser.yml`:

   - `terraform-provider-deed_<version>_<os>_<arch>.zip` for linux, darwin, windows and freebsd on amd64 and arm64,
     plus linux arm;
   - `terraform-provider-deed_<version>_SHA256SUMS` and its detached signature `…_SHA256SUMS.sig`;
   - `terraform-provider-deed_<version>_manifest.json` (from `terraform-registry-manifest.json`, protocol 6.0).

## Release checklist

1. `main` is green in CI (lint, unit, docs, release-config, acceptance and the homelab scenario).
2. Check the Deed compatibility: the pinned `DEED_COMMIT` (Makefile and `.github/workflows/test.yml`), the minimum
   server version in `internal/provider/provider.go` (`MinimumServerVersion`) and the table in `README.md` agree with
   go-deed's `docs/COMPATIBILITY.md`.
3. Move the `[Unreleased]` entries in `CHANGELOG.md` under the new version with today's date and update the links.
4. Dry-run locally (nothing is published, `dist/` is ignored by git):

   ```sh
   make release-check release-snapshot
   ```

5. Commit, then tag and push (a signed annotated tag is recommended):

   ```sh
   git tag -s v0.1.0 -m "v0.1.0"
   git push origin v0.1.0
   ```

6. Wait for the `release` workflow, open the draft release, check the assets listed above and paste the changelog
   section as release notes, then **Publish release**.
7. Verify the Registry picked it up (the provider page lists the version) and that a clean configuration installs it:

   ```sh
   terraform init   # with source = "aurimasniekis/deed", version = "0.1.0"
   ```

Versions follow semantic versioning: breaking schema or behaviour changes need a new major version (or, before 1.0, a
new minor version called out in the changelog); state-format changes additionally need a state upgrader (see
"State schema versions" in the README).

If a release is broken, do not delete or re-tag it (the Registry caches versions); publish a fixed patch release.

## Dry run record
2026-10-25 at `b2645ab`: `goreleaser release --snapshot --clean --skip=publish` with a throwaway GPG key in a
temporary keyring (deleted afterwards) produced the nine platform zips, `SHA256SUMS` listing them and the
`_manifest.json` (protocol `6.0`), and `SHA256SUMS.sig`, which `gpg --verify` accepted. Terraform 1.11.4 installed the
darwin/arm64 zip from a filesystem mirror (`provider_installation { filesystem_mirror { … } }`) and
`terraform providers schema -json` listed all six resources and six data sources. Nothing was published; the release
key, repository secrets and Registry namespace still have to be set up as described above.
