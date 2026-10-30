# Appliance (site) database migrations

`NNNN_name.up.sql` / `NNNN_name.down.sql`, applied in number order by `scripts/edge-migrate.sh`
(`deploy/scripts/apply-pending-migrations.sh` for an upgrade). `baseline/0000_production_baseline.sql` is the factory-clean
end state of `0001..NNNN`; a freshly provisioned appliance starts from it with an empty ledger.

## Three files make the sequence tamper-evident

| File | Meaning | Enforced by |
|---|---|---|
| `RESERVED_NUMBERS` | Number ranges held out of the sequence **on purpose** (one `FIRST-LAST reason` per line). Today `0182-0189`: set aside while the closure-round work ran in parallel, never used; `0190` was the first migration after the block. | CI (`tools/validate-migration-location.py`) **and** the runtime (`edge-migrate.sh`, `apply-pending-migrations.sh`) |
| `IDENTITIES` | `NNNN_name sha256(up file)` of every migration that has shipped. A shipped migration is never edited. | CI, and the runtime (a release whose file differs from its pin is refused) |
| `public.schema_migrations` (the database) | The full `NNNN_name` of every migration this database ran. | the runtime: a release that offers a different name for an applied number, or lacks an applied file, is refused |

### Reserved numbers

* A file named `NNNN_*.up.sql` or `.down.sql` with a reserved number is **refused**: by CI before merge, and at apply time by
  `edge-migrate.sh` and `apply-pending-migrations.sh` (the second reads the same `RESERVED_NUMBERS` before it plans anything, so a
  release built from a branch that skipped CI still cannot apply one). The Central tree (`control-plane/migrations`) has the same file and
  `deploy/scripts/central-migrate.sh` the same refusal.
* A reserved number is **not a gap**: the contiguity check skips it, and the runner's own "no lower migration unapplied" check walks
  migration *files* below the one it applies, never numbers, so the hole `0182..0189` is harmless when `0190+` are applied (measured by
  `scripts/ci/apply-pending-migrations-selftest.sh`).
* To hold more numbers out, add **one line** to `RESERVED_NUMBERS`. To use a number, never edit a range: take the next free number.

### Identities

* After a migration has shipped (merged and released), run `python3 tools/validate-migration-location.py --freeze` to append it to
  `IDENTITIES`. Until then it may be unlisted, but only above the highest listed number: an unlisted file *below* it fails CI.
* A listed migration whose content changes fails CI (`changed after it shipped`) and fails `edge-migrate.sh` / `apply-pending-migrations.sh`
  (`differs from the identity this release pins`). Fix forward with a new migration.
* The ledger keeps the migration **name** (`version`), not its hash. The hash is pinned by this file in the release instead, so the runtime
  compares: file hash against the pin, and ledger name against the release's file for that number. Adding a hash column to the ledger would be a
  platform schema change and a baseline regeneration, and was not done.

`python3 tools/validate-migration-location-selftest.py` proves each rule against a throwaway tree, including a negative case that adds
`0185_x.up.sql` and is refused.
