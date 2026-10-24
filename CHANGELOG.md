# Changelog

All notable changes to this project are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

First release, planned as 0.1.0. Requires Deed at or after commit `6422bee` (API `v1`) and Terraform 1.11 or newer.

### Added

- Provider configuration `endpoint`, `token` (sensitive), `source_name` and `ca_file`, also from `DEED_ENDPOINT`,
  `DEED_TOKEN`, `DEED_SOURCE` and `DEED_CA_FILE`; client-side token format check; warning when the server's API version
  is not `v1` or its release is older than supported.
- Resources `deed_project`, `deed_project_member`, `deed_kind`, `deed_relationship_type`, `deed_entity` and
  `deed_relationship`, all importable.
- Data sources `deed_project`, `deed_kind`, `deed_relationship_type`, `deed_entity`, `deed_entities` and
  `deed_relationship`.
- Entities and relationships are declarations through one Deed source: state mirrors only that declaration, and
  destroying a resource removes only its facet or assertion.
- Custom kind schema changes publish new schema versions, with `breaking` and `migration` for incompatible ones and
  Deed's compatibility problems as diagnostics.
- ETags in private state sent as `If-Match`; one `Idempotency-Key` per POST reused across retries; jittered retries
  honouring `Retry-After`; problem field errors mapped to attribute diagnostics.
- `examples/` for every resource and data source, the `examples/homelab` scenario and generated `docs/`.
- Release configuration for the Terraform Registry (GoReleaser, signed SHA256SUMS, registry manifest).

[Unreleased]: https://github.com/aurimasniekis/terraform-provider-deed/commits/main
