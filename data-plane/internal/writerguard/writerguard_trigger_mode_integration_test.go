//go:build integration

package writerguard

import (
	"context"
	"strings"
	"testing"
)

// P2 M10: a guard switched to ENABLE REPLICA fires only in replica mode, so it refuses nothing in normal operation and must fail the check.
func TestIntegration_ReplicaOnlyGuardIsRefused(t *testing.T) {
	owner := ownerPool(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `ALTER TABLE iam_v2.purchases ENABLE REPLICA TRIGGER p3_purchase_controlled_writer`); err != nil {
		t.Fatalf("switch the guard to replica mode: %v", err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), `ALTER TABLE iam_v2.purchases ENABLE TRIGGER p3_purchase_controlled_writer`)
	})
	probe := probeConn(t, owner)
	err := Verify(ctx, probe, []Requirement{{Family: "commerce_intent", Function: "iam_v2.begin_controlled_operation(text)",
		Tables: []string{"purchases"}, Tier: TierCapabilityScoped, Capability: CapCommerceIntent}})
	if err == nil || !strings.Contains(err.Error(), "no ENABLED controlled-writer guard") {
		t.Fatalf("a replica-only guard was accepted: %v", err)
	}
}

// P2 M10: an AFTER trigger runs once the write is done, so it is not a guard; and a role that owns a guarded table can disable it.
func TestIntegration_AfterTriggerAndTableOwnerAreRefused(t *testing.T) {
	owner := ownerPool(t)
	ctx := context.Background()
	if _, err := owner.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS iam_v2.wg_after_probe (id int);
		DROP TRIGGER IF EXISTS wg_after ON iam_v2.wg_after_probe;
		CREATE TRIGGER wg_after AFTER INSERT OR UPDATE OR DELETE ON iam_v2.wg_after_probe
		  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();
		CREATE TABLE IF NOT EXISTS iam_v2.wg_owned_probe (id int);
		DROP TRIGGER IF EXISTS wg_before ON iam_v2.wg_owned_probe;
		CREATE TRIGGER wg_before BEFORE INSERT OR UPDATE OR DELETE ON iam_v2.wg_owned_probe
		  FOR EACH ROW EXECUTE FUNCTION iam_v2.p3_controlled_writer_only();`); err != nil {
		t.Fatalf("seed the probe tables: %v", err)
	}
	t.Cleanup(func() {
		_, _ = owner.Exec(context.Background(), `DROP TABLE IF EXISTS iam_v2.wg_after_probe; DROP TABLE IF EXISTS iam_v2.wg_owned_probe`)
	})
	probe := probeConn(t, owner)
	if _, err := owner.Exec(ctx, `ALTER TABLE iam_v2.wg_owned_probe OWNER TO p3_guard_probe`); err != nil {
		t.Fatalf("hand the table to the probe role: %v", err)
	}
	fn := "iam_v2.begin_controlled_operation(text)"
	err := Verify(ctx, probe, []Requirement{{Family: "probe", Function: fn, Tables: []string{"wg_after_probe"}}})
	if err == nil || !strings.Contains(err.Error(), "no ENABLED controlled-writer guard") {
		t.Fatalf("an AFTER trigger was accepted as a guard: %v", err)
	}
	err = Verify(ctx, probe, []Requirement{{Family: "probe", Function: fn, Tables: []string{"wg_owned_probe"}}})
	if err == nil || !strings.Contains(err.Error(), "owns iam_v2.wg_owned_probe") {
		t.Fatalf("a role that owns a guarded table was accepted: %v", err)
	}
}
