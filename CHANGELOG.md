# Changelog

All notable changes to this project are documented in this file. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `deed_integration`: telemetry integrations (`prometheus`, `loki`, `grafana`, `natsmon`, `opensearch`) with JSON
  `config` and write-only `secrets_wo` / `secrets_wo_version`, as for `deed_source`. Deleting one deletes its bindings.
- `deed_telemetry_binding`: named query templates with typed parameters (JSON pointers into the entity), kinds, shape,
  unit, role (`chart`, `health`, `edge_rate`, `link`), thresholds, freshness and the relationship type of edge-rate
  bindings. Removing an argument returns it to Deed's default.
- `deed_integrations` data source.

## [0.3.1] - 2026-10-26

Works with Deed 0.1.0 or newer; `grouping_relationship_types` needs Deed 0.3.0. Tested against Deed v0.3.0.

### Fixed

- The 0.3.0 release was built from the 0.2.0 commit (the tag was created on the wrong commit), so it did not contain the
  changes below. 0.3.1 is the intended release; use it instead of 0.3.0.

### Added

- `deed_kind.grouping_relationship_types` (and on the `deed_kind` data source): makes a custom kind a map grouping
  (Deed 0.3.0 or newer). Omitting it leaves the grouping as set in Deed; an empty set removes it.

## [0.3.0] - 2026-10-26

Released from the wrong commit: identical to 0.2.0. Use 0.3.1.

## [0.2.0] - 2026-10-26

Works with Deed 0.1.0 or newer; the service account resources need Deed 0.2.0. Tested against Deed v0.2.0.

### Added

- `deed_source` resource: connectors (`github`, `hallpass`, `porch`, `terraform_state`, `pulse_admin`, `pulse_tail`, …)
  and push sources with `config` and `policy` as JSON, schedule and enabled flag. Credentials go in the write-only
  `secrets_wo` (never stored in plan or state; Terraform 1.11+), resent when `secrets_wo_version` changes. Import by
  `<project>/<name>` or ID.
- `deed_source` data source with sync status (freshness, running, last run and success, last error, next run).
- `deed_service_account` and `deed_service_account_token` resources (Deed 0.2.0 or newer): automation identities
  bounded by their installation role and project memberships (`deed_project_member` accepts their ID), and tokens that
  are recreated when revoked or expired, with `rotate_before_days` and `keepers` for scheduled rotation.

## [0.1.0] - 2026-10-26

First release. Requires Deed 0.1.0 or newer (API `v1`) and Terraform 1.11 or newer.

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

[Unreleased]: https://github.com/aurimasniekis/terraform-provider-deed/compare/v0.3.1...HEAD
[0.3.1]: https://github.com/aurimasniekis/terraform-provider-deed/compare/v0.2.0...v0.3.1
[0.3.0]: https://github.com/aurimasniekis/terraform-provider-deed/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/aurimasniekis/terraform-provider-deed/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/aurimasniekis/terraform-provider-deed/releases/tag/v0.1.0
