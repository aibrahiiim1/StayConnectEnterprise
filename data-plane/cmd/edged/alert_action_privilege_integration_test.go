package main

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// The Alerts screen's Acknowledge and Resolve run as svc_edged; the phase-3 fixture connects as a superuser and cannot see a missing grant.
func TestSvcEdgedMayRecordAlertActions(t *testing.T) {
	dsn := os.Getenv("EDGED_ROLE_TEST_DSN")
	if dsn == "" {
		t.Skip("EDGED_ROLE_TEST_DSN not set; skipping the svc_edged privilege proof")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	p, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect as svc_edged: %v", err)
	}
	defer p.Close()
	var ok bool
	if err := p.QueryRow(ctx, `SELECT has_function_privilege(current_user,
		'iam_v2.record_alert_action(uuid,uuid,uuid,text,uuid,text,text)', 'EXECUTE')`).Scan(&ok); err != nil {
		t.Fatalf("privilege check: %v", err)
	}
	if !ok {
		t.Fatal("svc_edged cannot execute iam_v2.record_alert_action: Acknowledge and Resolve would always be refused")
	}
}
