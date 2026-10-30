# Offline first activation: the package now carries the signed assignment trust registry

## Defect and evidence

A factory-clean appliance installed with `--offline-activation-only` (vendor licence key and registry root key
pinned, never online) refused a genuine, correctly signed activation package minted by a real Central:
`403 package rejected: the signed assignment inside it could not be verified (no assignment trust registry on this appliance)`.

Cause: `verifyPackageAssignment` (data-plane/cmd/scd/offline_first_activation.go) judges the assignment against the
registry on disk. The signed registry normally arrives only online (`/v1/appliance/assignment-registry`), so a
never-online appliance never has one, and the package did not carry it although the documentation promised the
package carries "the trust material". The earlier unit tests passed only because they wrote a registry to disk first;
the new tests start with none.

## Design

* `activation.Package.AssignmentRegistry` (`assignment_registry`, `json.RawMessage`, `omitempty`), the last field of the
  signed view in both `internal/activation` copies. It holds the `SignedRegistry` document exactly as Central serves it.
* Central: `currentRegistryEnvelope` (assignment_registry.go) is the one query behind both `RegistryBase.Serve` and
  `activationPackage`. The package carries that document, parsed and re-marshalled through the signed struct (jsonb
  reorders keys; the root signature covers the struct fields only, so the signature still verifies). No row -> field left out;
  any other read error -> 503, never a package the appliance might not be able to verify.
* Appliance: `vetPackageRegistry` verifies a carried registry with the new `RegistryStore.Check`, the checks
  `RegistryStore.Adopt` already made (now factored out of it, behaviour unchanged): signature against the **pinned root**,
  validity window, lower version than held refused, equal version only if identical. It is vetted before the assignment is
  judged (`verifyPackageAssignment`, nothing written) and again at write time inside `applyActivationPackage`.
* Persistence: `adoptPackageRegistry` calls `RegistryStore.Adopt` (same previous/marker handling as online) as step 2a of the
  all-or-nothing apply, after the journal and the single-use ledger, before the assignment. Before touching anything it durably
  records the prior registry bytes and marker state in `activation-registry-undo.json`. Every later failure path
  (`undoAssignment`) restores them; `recoverInterruptedActivation` restores them on a rolled-back or unreadable journal, and
  discards the record when the activation had completed or when no journal exists.

## Wire compatibility

`omitempty` on an empty `RawMessage` drops the key, so a package without the field serializes to the same bytes as before:
`TestGoldenVectorRequestAndPackage` is unchanged and passes in both modules. `SchemaVersion` stays 1. New vector
`TestGoldenVectorPackageWithRegistry` (files `golden_vector_registry_test.go` and `..._values_test.go`, byte-identical in both
modules, same signature computed independently in each). An appliance holding a registry keeps accepting registry-less packages.
Direction that does NOT work: an appliance running the old code receiving a package that carries the field verifies the signature
over bytes without it and refuses ("signature invalid"). Central now always includes the registry when one is signed, so an
appliance must run this scd before it can take a package minted by this Central. (An old appliance that already held a registry
is affected too; it fails closed, nothing is applied.)

## Security argument

* The registry is anchored on the pinned root key, never on the package signature. The vendor signature proves only who sent the
  file; the root signature decides whether the keys listed are the assignment signers. A package cannot introduce keys the root did not sign.
* No new way to re-point an assigned appliance: `AcceptForFirstActivation` still refuses an assigned appliance first, and a carried
  registry is only reached after it. The registry-only effect on an unassigned appliance is the same as an online refresh.
* Rollback: a lower version than held is refused; an equal version with different content is refused. A held file is compared as
  `Adopt` always compared it.
* No root key pinned + a carried registry -> refused (nothing to verify it against).
* Adopting a signed registry sets the one-way marker that disables the legacy unsigned trust file, exactly as an online adoption does.
* All-or-nothing: nothing written by a refused package; a failed or interrupted apply restores the registry byte for byte (or removes it and the marker).

## Tests (all run)

data-plane (Linux container, golang:1.26.10): `cmd/scd`, `internal/assignment`, `internal/activation`, `internal/offline`; control-plane:
`internal/activation`, `internal/assignment`, `internal/offline`, and the DB-backed `internal/api` offline/activation/registry/assignment
tests via `scripts/ci/central-restricted-role-tests.sh`. New scd tests (`offline_first_activation_registry_test.go`): factory-clean + genuine
package with registry applies and the assignment re-verifies at boot; no registry anywhere + registry-less package still refused; forged root;
registry edited after root signing; registry edited after package signing (package signature refuses); no pinned root; older than held
refused with the held one untouched; newer replaces and keeps previous; registry-less package with registry on disk applies; failed apply
restores the prior registry exactly / leaves none; interrupted activation recovery restores/removes; second submission refused. Central
integration test `TestActivationPackageCarriesTheSignedRegistry`: the minted package carries a registry that verifies against Central's
registry root and equals what is served; absent when none is signed.

## Rollback

Revert the commit. Packages minted afterwards no longer carry the registry; packages already minted with it are refused by reverted
appliances (signature) and should be regenerated. A registry adopted by an activation is an ordinary adopted registry and needs no cleanup.
