# Terraform provider for Deed

Manage a [Deed](https://github.com/aurimasniekis/go-deed) installation — a declarative infrastructure catalog and live
relationship map — as code: projects and their members, custom kinds (with versioned JSON Schemas) and relationship
types, and above all the **entities** of your infrastructure and the **relationships** between them.

Built with [terraform-plugin-framework](https://github.com/hashicorp/terraform-plugin-framework) (protocol 6) and
tested against [Deed v0.4.0](https://github.com/aurimasniekis/go-deed/releases/tag/v0.4.0) with Terraform 1.11; it works
with every Deed release from 0.1.0 on (service accounts need 0.2.0, kind map grouping 0.3.0, the telemetry binding
arguments `guard`, `when_empty` and `on_map` 0.4.0).

## Repository name and source address

| | |
|---|---|
| Repository | `github.com/aurimasniekis/terraform-provider-deed` (Go module of the same path) |
| Registry address | `registry.terraform.io/aurimasniekis/deed` |
| Source address in configurations | `aurimasniekis/deed` |
| Resource prefix | `deed_` |

Terraform derives the provider type (`deed`) from the repository name `terraform-provider-<type>`; configurations
always refer to the source address, never to the repository.

## Installation

The provider is not published to the Terraform Registry yet. Build it and point Terraform at the binary with a
[development override](https://developer.hashicorp.com/terraform/cli/config/config-file#development-overrides-for-provider-developers):

```sh
make build          # bin/terraform-provider-deed (or `make install` for $GOBIN)
```

```hcl
# ~/.terraformrc (or a file named by TF_CLI_CONFIG_FILE)
provider_installation {
  dev_overrides {
    "aurimasniekis/deed" = "/path/to/terraform-provider-deed/bin"
  }
  direct {}
}
```

With a development override `terraform init` is not needed for this provider; `terraform plan` warns that overrides
are in effect. Once published, remove the override and pin a version:

```hcl
terraform {
  required_providers {
    deed = {
      source  = "aurimasniekis/deed"
      version = "~> 0.1"
    }
  }
}
```

## Configuration

```hcl
provider "deed" {
  endpoint = "https://deed.example.com" # or DEED_ENDPOINT
  token    = var.deed_token             # or DEED_TOKEN
  # source_name = "terraform"           # or DEED_SOURCE
  # ca_file     = "/etc/ssl/private-ca.pem" # or DEED_CA_FILE
}
```

| Argument | Environment | Description |
|---|---|---|
| `endpoint` | `DEED_ENDPOINT` | Public URL of Deed (`/api/v1` is appended). |
| `token` (sensitive) | `DEED_TOKEN` | User API token: `deed_at_` followed by 43 base62 characters. The format is checked locally without ever echoing the token. |
| `source_name` | `DEED_SOURCE` | Deed source every entity and relationship is declared through (default `terraform`). Terraform reserves the argument name `source` in provider blocks, hence `source_name`. |
| `ca_file` | `DEED_CA_FILE` | PEM bundle trusted in addition to the system roots. |

Unknown (computed) provider arguments are rejected; missing ones are reported per argument. At configure time the
provider reads `GET /api/v1/system/info` and **warns** (it does not fail) when the server reports an API version other
than `v1` or a release older than the supported minimum.

**Tokens.** Prefer a **service account** token for automation: a non-human Deed user that cannot sign in and is bounded
by its own installation role and project memberships. Get the first one on the Deed host with
`deed admin token -service-account terraform -create -role admin -days 366` (server stopped; prints only the token), or
in the UI under *Settings → Users → Service accounts*; after that Terraform can manage service accounts and rotate tokens
itself (`deed_service_account`, `deed_service_account_token`; Deed 0.2.0 or newer). Personal API tokens (the account
page, or `POST /api/v1/me/tokens` with a recent sign-in) work too and act with at most that user's roles. Either way:

- projects, kinds and relationship types need a token with the installation `admin` role (owners can issue one);
- entities, relationships and project members need the project `editor` (members: `admin`) role.

> Deed refuses unknown `DEED_*` environment variables at start-up. Deed before
> [`40311d6`](https://github.com/aurimasniekis/go-deed/commit/40311d64183c) did not know the provider's `DEED_SOURCE`
> and `DEED_CA_FILE`; with such a server, unset them for the server process.

## Resources and data sources

| Resource | Import ID |
|---|---|
| `deed_project` | project name or ID |
| `deed_project_member` | `<project>/<user_id>` |
| `deed_kind` | kind name, e.g. `example.com/ups` |
| `deed_relationship_type` | type name |
| `deed_entity` | entity ID, or `<project>/<kind>/<name>` (the kind may contain a slash) |
| `deed_relationship` | relationship ID (`rel_…`) |
| `deed_source` | `<project>/<name>` or source ID (`src_…`) |
| `deed_service_account` | name or user ID (`usr_…`) |
| `deed_service_account_token` | not importable (the secret cannot be read back) |
| `deed_integration` | name or ID |
| `deed_telemetry_binding` | name or ID |

Data sources: `deed_project`, `deed_kind`, `deed_relationship_type`, `deed_entity` (the **effective** view, by ID or
by project/kind/name, including computed status as JSON), `deed_entities` (search by project, kind, labels and full
text), `deed_relationship` (by ID or by type and endpoints) and `deed_source` (configuration and sync status: freshness,
last run, last error) and `deed_integrations` (telemetry integrations, by adapter).

Every resource and data source has an example under [`examples/`](examples); the generated reference is in
[`docs/`](docs). [`examples/homelab`](examples/homelab/main.tf) is a complete scenario: a custom `UPS` kind and
`powers` relationship type, two servers, a media service, the repository it is deployed from (with a GitHub link and a
Grafana dashboard link on the service), the owning team, and the relationships between them.

## How the provider models Deed

- **Declarations through one source.** Deed merges the *facets* of every source that knows an entity — Terraform and
  API declarations, importers, runtime observations, UI annotations — into an effective view with provenance. The
  provider writes through one source (`terraform`): `deed_entity` creates the declaration with `POST /entities`,
  reads it back from `GET /entities/{id}/declarations/terraform`, replaces it with `PUT` and removes it with
  `DELETE /entities/{id}/declarations/terraform`.
- **State is the declaration, never the merged view.** Values contributed by other sources and observed status
  (health, freshness, observed values) therefore never cause a diff; they are exposed by the `deed_entity` *data
  source* instead. The resource deliberately has no volatile computed attributes.
- **Destroy removes only Terraform's facet.** Imports, observations and annotations of the same entity survive; the
  entity is tombstoned only when no source contributes to it any more. Relationships work the same way: the resource
  manages the `terraform` assertion and treats the relationship as gone when that assertion is missing or not live.
- **Relationships are declared from the `from` entity's project.** Deed records an assertion through the source of
  the declaring project but updates and retracts it through the source of the relationship's (`from` entity's)
  project, so `deed_relationship.project` must be that project (it defaults to it); other values are refused.
- **The entity ID is the identity.** When Deed adopts an existing entity (an identity-key match against an entity
  reported under another name, or an alias after a rename), the configured `kind` and `name` stay in the state.
- **Adopting vs. conflicting.** Creating an entity that other sources already reported adopts it. If the `terraform`
  source already declares it — another resource or configuration — the apply fails with
  `Entity is already declared by source "terraform"` and the `terraform import` command to use instead.
- **Names are immutable.** Changing an entity's `project`, `kind` or `name` replaces the declaration (renames go through
  Deed's rename API and keep the old name as an alias; the provider does not perform them).
- **JSON documents.** `spec`, kind `schema`, relationship `attributes` and `attributes_schema` are JSON strings
  (`jsonencode`) of type `jsontypes.Normalized`. Custom-kind properties are validated server-side against the kind's
  JSON Schema, so they are not modelled as Terraform attributes; Deed stores documents re-encoded (sorted keys,
  compact) and semantic equality keeps that from ever causing a diff.
- **Schema versions.** Changing `deed_kind.schema` publishes a new schema version. Non-breaking versions must be
  backward compatible (Deed lists the problems otherwise); `breaking = true` with `migration = "revalidate"` or
  `"mark_invalid"` publishes incompatible ones. Built-in kinds and relationship types cannot be managed.
- **Optimistic concurrency.** The ETag of every versioned object is kept in private state and sent as `If-Match` on
  PUT/PATCH; a change made outside Terraform since the last refresh fails with `412` and advice to refresh.
- **Safe retries.** Every POST carries one random `Idempotency-Key`, reused across the provider's own retries.
  Network errors, 5xx, 429 and 503 are retried with jittered exponential backoff that honours `Retry-After`;
  conditional writes are retried only when the server certainly did not apply them. Redirects are never followed,
  responses are capped at 8 MiB, and request/response bodies are never logged.
- **Diagnostics.** Problem documents (RFC 9457) are turned into diagnostics; per-field validation errors point at the
  offending attribute (for example `labels["Team"]` or `links[1].url`).
- **Sources and credentials.** `deed_source` manages connectors (`github`, `hallpass`, `porch`, `terraform_state`,
  `pulse_admin`, `pulse_tail`, …) and push sources. `config` and `policy` are JSON strings validated by Deed against the
  provider's schema. Credentials go in `secrets_wo`, a write-only argument (Terraform 1.11+) that is sent to Deed but
  never stored in the plan or state (Deed never returns it either); change `secrets_wo_version` to send new values.
  Sync status is volatile, so it is only on the `deed_source` data source. Destroying a source deletes every fact it
  contributed.
- **Service account tokens are in state.** `deed_service_account_token.token` is a sensitive computed value and therefore
  stored in the state; keep the state encrypted, or create tokens outside Terraform. Tokens revoked or expired outside
  Terraform are recreated; `rotate_before_days` and `keepers` replace them on schedule (use `create_before_destroy`).
- **Tombstones and definitions.** Deleting a kind or relationship type is blocked only by live users; tombstoned
  entities and retracted relationships are compacted with it.

## Compatibility

| Provider | Deed | API | Terraform |
|---|---|---|---|
| 0.4.x | ≥ 0.1.0; service accounts need ≥ 0.2.0, `deed_kind.grouping_relationship_types` ≥ 0.3.0 | `/api/v1` | ≥ 1.11 (tested 1.11.4) |
| 0.3.1 | ≥ 0.1.0; service accounts need ≥ 0.2.0, `deed_kind.grouping_relationship_types` ≥ [0.3.0](https://github.com/aurimasniekis/go-deed/releases/tag/v0.3.0) | `/api/v1` | ≥ 1.11 (tested 1.11.4) |
| 0.2.x | ≥ 0.1.0; `deed_service_account` and `deed_service_account_token` need ≥ [0.2.0](https://github.com/aurimasniekis/go-deed/releases/tag/v0.2.0) | `/api/v1` | ≥ 1.11 (tested 1.11.4) |
| 0.1.x | ≥ [0.1.0](https://github.com/aurimasniekis/go-deed/releases/tag/v0.1.0) | `/api/v1` | ≥ 1.11 (tested 1.11.4) |

The provider and Deed are versioned and released independently. The provider only talks to `/api/v1`, which changes
additively, so a provider release works with every Deed release at or above its minimum; see go-deed's
[`docs/COMPATIBILITY.md`](https://github.com/aurimasniekis/go-deed/blob/main/docs/COMPATIBILITY.md). CI pins the Deed
commit the acceptance tests run against (`DEED_COMMIT` in the Makefile and `.github/workflows/test.yml`).

## State schema versions

Every resource schema is at version 0. A change that alters how existing state must be read (renaming or removing an
attribute, changing its type or meaning) bumps that resource's version and adds a state upgrader for the previous
version; upgraders are never removed. Additive changes do not bump the version. The plumbing lives in
[`internal/provider/upgrade.go`](internal/provider/upgrade.go).

## Development

| Command | |
|---|---|
| `make test` | Unit tests: client (headers, If-Match, idempotency, retry rules, problems, redaction), schemas, configuration, JSON semantics |
| `make testacc` | Builds `bin/deed` from the pinned commit and runs the acceptance tests against a real `deed serve` |
| `make scenario` | Applies `examples/homelab` with `dev_overrides`, requires an empty second plan, destroys it |
| `make lint` | golangci-lint |
| `make docs` / `make docs-check` | Generate / verify `docs/` with tfplugindocs v0.25.0 |
| `make release-check` / `make release-snapshot` | Validate / dry-run the GoReleaser configuration (nothing is published) |

`make deed-src` exports the pinned Deed commit from a sibling checkout (`DEED_REPO ?= ../deed`) with `git archive`
into `.deed-src/`; `make deed-bin` builds `bin/deed` from it. The acceptance harness starts that binary in development
mode on free loopback ports with a private data directory, completes the bootstrap link as a local owner, enrolls
TOTP and issues API tokens. `scripts/verify-scenario.sh` does the same, or uses a running Deed when `DEED_ENDPOINT`
and `DEED_TOKEN` are set.

Releasing is described in [`docs/RELEASING.md`](docs/RELEASING.md); changes are listed in
[`CHANGELOG.md`](CHANGELOG.md).

## License

Apache License 2.0 — see [`LICENSE`](LICENSE) and [`NOTICE`](NOTICE). Portions are adapted from
[terraform-provider-porch](https://github.com/aurimasniekis/terraform-provider-porch) (Apache-2.0).
