//go:build integration

package stayengine

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func withAttrs(payload, attrs string) string {
	return strings.TrimSuffix(payload, "}") + "," + attrs + "}"
}

func attrsOf(t *testing.T, p *pgxpool.Pool, s scope, res string) (vip sql.NullBool, agent sql.NullString, room string) {
	t.Helper()
	if err := p.QueryRow(context.Background(), `SELECT vip, travel_agent, COALESCE(normalized_room_number,'')
		FROM iam_v2.stays WHERE pms_interface_id=$1 AND external_reservation_id=$2`, s.iface, res).Scan(&vip, &agent, &room); err != nil {
		t.Fatalf("read stay: %v", err)
	}
	return
}

func TestIntegration_GuestAttributesFollowWhatThePMSStates(t *testing.T) {
	p := pool(t)
	defer p.Close()
	s := seed(t, p)
	pr := NewProcessor(p)
	res := "RES-ATTR"

	insertLive(t, p, s, "a-gi", "GI", withAttrs(pay(res, "2101", "Doe", "Jane", "", "260101", "260110"),
		`"travel_agent":"Sunny Tours","vip":true`))
	process(t, pr, s)
	if vip, agent, _ := attrsOf(t, p, s, res); !vip.Valid || !vip.Bool || agent.String != "Sunny Tours" {
		t.Fatalf("after GI: vip=%v agent=%v", vip, agent)
	}

	insertLive(t, p, s, "a-gc1", "GC", pay(res, "2101", "Doe", "Janet", "", "260101", "260110"))
	process(t, pr, s)
	if vip, agent, _ := attrsOf(t, p, s, res); !vip.Bool || agent.String != "Sunny Tours" {
		t.Fatalf("an unstated attribute changed: vip=%v agent=%v", vip, agent)
	}

	insertLive(t, p, s, "a-gc2", "GC", withAttrs(pay(res, "2101", "Doe", "Janet", "", "260101", "260110"),
		`"travel_agent":"","vip":false`))
	process(t, pr, s)
	if vip, agent, _ := attrsOf(t, p, s, res); !vip.Valid || vip.Bool || agent.Valid {
		t.Fatalf("stated no-VIP/no-agent: vip=%v agent=%v", vip, agent)
	}

	insertLive(t, p, s, "a-mv", "GC", withAttrs(pay(res, "2102", "Doe", "Janet", "", "260101", "260110"),
		`"travel_agent":"Blue Sea","vip":true`))
	process(t, pr, s)
	if vip, agent, room := attrsOf(t, p, s, res); room != "2102" || !vip.Bool || agent.String != "Blue Sea" {
		t.Fatalf("after room move: room=%s vip=%v agent=%v", room, vip, agent)
	}
	if st, _ := eventOutcome(t, p, s, "a-mv"); st != "APPLIED" {
		t.Fatalf("room move outcome=%s", st)
	}
}
