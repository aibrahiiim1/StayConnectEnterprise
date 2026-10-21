//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// Migration 0146: an assisted guest data request. A MAC address, a room number, a reservation credential or a shared
// device is not proof of who a person is. Staff verify the requester and state what they saw; the database
// classifies every record in the stated range, and discloses or anonymises exactly the records staff reviewed and
// selected -- only those established as the person's -- and nothing that belongs to another person.

// asstRoom is one reservation (CHECKED_OUT, the primary guest "Ada Lovelace") with the room's ended access and n
// devices, each with one ended session. The addresses are global (not randomised) unless private.
type asstRoom struct {
	stay, room string
	devs, sess []string
	macs, ips  []string
}

func asstMACs(prefix string, n int) (macs []string) {
	for i := 1; i <= n; i++ {
		macs = append(macs, fmt.Sprintf("%s:%02x", prefix, i))
	}
	return
}

func seedAsstRoom(t *testing.T, f *apiFixture, tag string, macs []string, ipBase string) asstRoom {
	t.Helper()
	sfx := fmt.Sprint(time.Now().UnixNano())
	r := asstRoom{macs: macs}
	r.stay = seedGuestDataStay(t, f, "R-"+tag+"-"+sfx, "CHECKED_OUT")
	r.room = newAccess(t, f, tag+sfx, "stay_id", r.stay, false)
	for i, m := range macs {
		d := newDevice(t, f, m)
		ip := fmt.Sprintf("%s.%d", ipBase, i+1)
		r.devs = append(r.devs, d)
		r.ips = append(r.ips, ip)
		r.sess = append(r.sess, onDevice(t, f, r.room, d, ip, "PMS", false))
	}
	return r
}

func (r asstRoom) anchor() []map[string]any {
	return []map[string]any{{"kind": "stay", "id": r.stay}}
}

type asstOpts struct {
	purpose    string
	macs       []string
	anchors    []map[string]any
	from, to   time.Time
	device     bool
	soleUse    bool
	private    bool
	identity   bool
	reason     string
	requester  string
	method     string
	evidence   string
	noRangeSet bool
}

func asstDefaults(purpose string, macs []string, anchors []map[string]any) asstOpts {
	now := time.Now()
	return asstOpts{purpose: purpose, macs: macs, anchors: anchors, from: now.Add(-6 * 24 * time.Hour), to: now.Add(-12 * time.Hour),
		device: true, soleUse: true, identity: true, reason: "guest asked in person at the desk",
		requester: "Ms R. Example", method: "ID_CHECKED_IN_PERSON", evidence: "identity card viewed and compared with the face; phone shown with its address"}
}

func (o asstOpts) body() map[string]any {
	return map[string]any{
		"purpose": o.purpose, "addresses": o.macs, "anchors": o.anchors,
		"range_from": o.from.UTC().Format(time.RFC3339), "range_to": o.to.UTC().Format(time.RFC3339),
		"requester": o.requester, "verification_method": o.method, "evidence_note": o.evidence,
		"statements": map[string]any{"identity_verified": o.identity, "device_shown": o.device, "sole_use": o.soleUse,
			"private_address_confirmed": o.private},
		"reason": o.reason,
	}
}

func asstOpen(t *testing.T, f *apiFixture, o asstOpts) (id, ref string) {
	t.Helper()
	st, body := f.do(t, "POST", "/guest-data-rights/assisted", o.body())
	if st != 201 {
		t.Fatalf("open the request = %d %v", st, body)
	}
	id, _ = body["id"].(string)
	ref, _ = body["ref"].(string)
	return
}

type asstRecord struct {
	Kind       string         `json:"kind"`
	ID         string         `json:"id"`
	Class      string         `json:"class"`
	Reason     string         `json:"reason"`
	Selectable bool           `json:"selectable"`
	Block      *string        `json:"erase_block"`
	Detail     map[string]any `json:"detail"`
}

type asstReview struct {
	Signature string         `json:"signature"`
	ByClass   map[string]int `json:"by_class"`
	Records   []asstRecord   `json:"records"`
}

func asstReviewOf(t *testing.T, f *apiFixture, id string) asstReview {
	t.Helper()
	st, raw := f.doRaw(t, "GET", "/guest-data-rights/assisted/"+id+"/review", nil)
	if st != 200 {
		t.Fatalf("review = %d %s", st, raw)
	}
	var rv asstReview
	if err := json.Unmarshal([]byte(raw), &rv); err != nil {
		t.Fatalf("review is not JSON: %v", err)
	}
	return rv
}

func (rv asstReview) of(kind string) []asstRecord {
	var out []asstRecord
	for _, r := range rv.Records {
		if r.Kind == kind {
			out = append(out, r)
		}
	}
	return out
}

func (rv asstReview) find(kind, id string) *asstRecord {
	for i := range rv.Records {
		if rv.Records[i].Kind == kind && rv.Records[i].ID == id {
			return &rv.Records[i]
		}
	}
	return nil
}

func (rv asstReview) selectable() []map[string]any {
	out := []map[string]any{}
	for _, r := range rv.Records {
		if r.Selectable {
			out = append(out, map[string]any{"kind": r.Kind, "id": r.ID})
		}
	}
	return out
}

func sel(kind, id string) map[string]any { return map[string]any{"kind": kind, "id": id} }

func asstAct(f *apiFixture, t *testing.T, verb, id string, rv asstReview, ref string, selected []map[string]any) (int, string) {
	t.Helper()
	return f.doRaw(t, "POST", "/guest-data-rights/assisted/"+id+"/"+verb, map[string]any{
		"password": f.password, "reason": "guest asked in person at the desk", "confirm": ref, "signature": rv.Signature, "selected": selected})
}

func asstStatus(t *testing.T, f *apiFixture, id string) map[string]any {
	t.Helper()
	st, body := f.do(t, "GET", "/guest-data-rights/assisted/"+id, nil)
	if st != 200 {
		t.Fatalf("get the request = %d %v", st, body)
	}
	return body
}

func asstClasses(rv asstReview, kind string) []string {
	var out []string
	for _, r := range rv.of(kind) {
		out = append(out, r.Class+":"+r.Reason)
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------
// The workflow: a phone in a shared room, claimed, reviewed and handed over alone.
// ---------------------------------------------------------------------------------------------------------
func TestIntegration_Assisted_AClaimedPhoneOfASharedRoomIsHandedOverAlone(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS1", asstMACs("3c:22:fb:10:00", 3), "10.90.1")
	id, ref := asstOpen(t, f, asstDefaults("ACCESS", r.macs[:1], r.anchor()))

	rv := asstReviewOf(t, f, id)
	if got := asstClasses(rv, "session"); len(got) != 1 || got[0] != "CLAIMABLE:access_of_the_stated_context" {
		t.Fatalf("the room's session on the claimed phone = %v, want one claimable", got)
	}
	if got := asstClasses(rv, "device"); len(got) != 1 || !strings.HasPrefix(got[0], "CLAIMABLE:") {
		t.Fatalf("the claimed phone = %v", got)
	}
	if len(rv.Records) != 2 {
		t.Fatalf("only the stated address is reviewed, got %d records: %+v", len(rv.Records), rv.Records)
	}
	// What would be disclosed is shown to staff before they decide.
	if d := rv.of("session")[0].Detail; d["ip"] != r.ips[0] {
		t.Errorf("the review does not show what the record would disclose: %v", d)
	}

	// --- A wrong confirmation, a stale review and a record that is not listed are all refused; nothing is handed over.
	if st, _ := asstAct(f, t, "export", id, rv, "AR-0000000000", rv.selectable()); st != 409 {
		t.Errorf("export with the wrong reference = %d, want 409", st)
	}
	bad := rv
	bad.Signature = strings.Repeat("0", 32)
	if st, raw := asstAct(f, t, "export", id, bad, ref, rv.selectable()); st != 409 || !strings.Contains(raw, `"changed"`) {
		t.Errorf("export of a review that is not the current one = %d %s", st, raw)
	}
	if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("session", r.sess[1])}); st != 409 || !strings.Contains(raw, "not_listed") {
		t.Errorf("export of another phone's session = %d %s", st, raw)
	}
	if st, _ := f.do(t, "POST", "/guest-data-rights/assisted/"+id+"/export", map[string]any{
		"password": "wrong", "reason": "guest asked", "confirm": ref, "signature": rv.Signature, "selected": rv.selectable()}); st != 401 {
		t.Errorf("export with a wrong password = %d, want 401", st)
	}

	// --- The hand-over: exactly the claimed phone; none of the other occupants' phones, none of the reservation.
	st, raw := asstAct(f, t, "export", id, rv, ref, rv.selectable())
	if st != 200 {
		t.Fatalf("export = %d %s", st, raw)
	}
	for _, leak := range []string{r.macs[1], r.macs[2], r.ips[1], r.ips[2], "Lovelace", "Ada", "R-AS1-", "identity card viewed"} {
		if strings.Contains(raw, leak) {
			t.Errorf("the hand-over carries %q, which is not the requester's", leak)
		}
	}
	for _, want := range []string{r.macs[0], r.ips[0], ref, "stayconnect.assisted-guest-data-export/1"} {
		if !strings.Contains(raw, want) {
			t.Errorf("the hand-over lacks %q: %s", want, raw)
		}
	}
	// Reading changes nothing.
	for i := range r.macs {
		if got := sessAddr(t, f, r.sess[i]); got != r.ips[i]+","+r.macs[i] {
			t.Errorf("an export changed session %d: %s", i, got)
		}
	}
	// Repeating the hand-over is allowed while the request is open; completing needs one.
	if got := asstStatus(t, f, id); got["status"] != "OPEN" {
		t.Fatalf("an exported request stays open until closed: %v", got["status"])
	}
	st, body := f.do(t, "POST", "/guest-data-rights/assisted/"+id+"/close", map[string]any{"outcome": "COMPLETED", "reason": "records handed over in person"})
	if rc, _ := body["receipt"].(map[string]any); st != 200 || rc["reference"] != ref || !strings.Contains(fmt.Sprint(rc["evidence_note"]), "identity card viewed") || rc["records_disclosed"] != float64(2) {
		t.Fatalf("close = %d %v: the hotel's file of the request must be handed back once", st, body)
	}
	got := asstStatus(t, f, id)
	if got["status"] != "COMPLETED" || got["addresses"] != nil || got["requester"] != "[removed]" || got["evidence_note"] != nil {
		t.Errorf("a closed request must keep no address and no free text: %v", got)
	}
	if st, raw := asstAct(f, t, "export", id, rv, ref, rv.selectable()); st != 409 || !strings.Contains(raw, "closed") {
		t.Errorf("export from a closed request = %d %s", st, raw)
	}
	// Nothing is left that links the closed request to a person: no anchor, and no id of a record that was only
	// handed over (it is still identifiable). The events keep counts.
	var anchors, ids int
	if err := f.pool.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM iam_v2.assisted_guest_request_anchors WHERE request_id=$1::uuid),
		(SELECT count(*) FROM iam_v2.assisted_guest_request_records WHERE request_id=$1::uuid)`, id).Scan(&anchors, &ids); err != nil || anchors != 0 || ids != 0 {
		t.Errorf("a closed hand-over request must keep no anchor and no record id: %d %d %v", anchors, ids, err)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.assisted_guest_request_events WHERE request_id=$1::uuid AND action='EXPORTED' AND detail->>'records'='2'`, id); got != "1" {
		t.Errorf("the hand-over event must keep its count: %s", got)
	}
}

// ---------------------------------------------------------------------------------------------------------
// Staff statements gate everything that is shared by nature.
// ---------------------------------------------------------------------------------------------------------
func TestIntegration_Assisted_NothingSharedIsSelectableWithoutTheStatements(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS2", asstMACs("3c:22:fb:20:00", 1), "10.90.2")
	for name, mod := range map[string]func(*asstOpts){
		"the device was not shown": func(o *asstOpts) { o.device = false },
		"sole use was not stated":  func(o *asstOpts) { o.soleUse = false },
		"neither was stated":       func(o *asstOpts) { o.device, o.soleUse = false, false },
	} {
		t.Run(name, func(t *testing.T) {
			o := asstDefaults("ACCESS", r.macs, r.anchor())
			mod(&o)
			id, ref := asstOpen(t, f, o)
			rv := asstReviewOf(t, f, id)
			if got := asstClasses(rv, "session"); len(got) != 1 || got[0] != "SHARED:statements_missing" {
				t.Fatalf("session = %v, want shared (statements missing)", got)
			}
			if len(rv.selectable()) != 0 {
				t.Fatalf("something is selectable without the statements: %v", rv.selectable())
			}
			if st, raw := asstAct(f, t, "export", id, rv, ref, []map[string]any{sel("session", r.sess[0])}); st != 409 || !strings.Contains(raw, "not_eligible") {
				t.Errorf("export without the statements = %d %s", st, raw)
			}
		})
	}
	// The requester must be verified before anything is opened.
	o := asstDefaults("ACCESS", r.macs, r.anchor())
	o.identity = false
	if st, _ := f.do(t, "POST", "/guest-data-rights/assisted", o.body()); st != 400 {
		t.Errorf("a request for an unverified requester = %d, want 400", st)
	}
}

// A randomised (private) address names a network profile, not a person: staff must confirm what the device showed.
func TestIntegration_Assisted_APrivateAddressNeedsItsOwnConfirmation(t *testing.T) {
	f := newAPI(t)
	r := seedAsstRoom(t, f, "AS3", []string{"2a:11:22:30:00:01"}, "10.90.3")
	o := asstDefaults("ACCESS", r.macs, r.anchor())
	id, _ := asstOpen(t, f, o)
	if got := asstClasses(asstReviewOf(t, f, id), "session"); len(got) != 1 || got[0] != "SHARED:private_address_not_confirmed" {
		t.Errorf("a private address without its confirmation = %v", got)
	}
	o.private = true
	id, _ = asstOpen(t, f, o)
	if got := asstClasses(asstReviewOf(t, f, id), "session"); len(got) != 1 || !strings.HasPrefix(got[0], "CLAIMABLE:") {
		t.Errorf("a confirmed private address = %v", got)
	}
}

// ---------------------------------------------------------------------------------------------------------
// The erasure: exactly the selection, nothing the room or another person shares.
// ---------------------------------------------------------------------------------------------------------
func TestIntegration_Assisted_ErasureAnonymisesTheSelectedPhoneAndNothingElse(t *testing.T) {
	f := newAPI(t)
	ctx := context.Background()
	r := seedAsstRoom(t, f, "AS4", asstMACs("3c:22:fb:40:00", 3), "10.90.4")
	// The activity log holds the claimed phone's address (a staff search, in whatever spelling the staff member typed)
	// and another occupant's.
	c := strings.ReplaceAll(r.macs[0], ":", "")
	spellings := []string{r.macs[0], strings.ToUpper(r.macs[0]), strings.ReplaceAll(r.macs[0], ":", "-"), c, c[0:4] + "." + c[4:8] + "." + c[8:12]}
	spellings = append(spellings, r.macs[0][6:]) // a search typed half way
	for _, sp := range spellings {
		if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
			VALUES (now() - interval '1 day',$1,'operator','op-1','guest_data.searched','guest_data',NULL,jsonb_build_object('search',$2::text))`,
			f.tenant, sp); err != nil {
			t.Fatalf("seed audit: %v", err)
		}
	}
	if _, err := f.pool.Exec(ctx, `INSERT INTO public.audit_log(ts,tenant_id,actor_type,actor_id,action,target_type,target_id,payload)
		VALUES (now() - interval '1 day',$1,'operator','op-1','guest_data.searched','guest_data',NULL,jsonb_build_object('search',$2::text))`,
		f.tenant, r.macs[1]); err != nil {
		t.Fatalf("seed audit: %v", err)
	}
	var consumedBefore string
	consumedBefore = gdStr(t, f, `SELECT concat_ws(',', status, consumed_data_bytes::text, consumed_online_seconds::text, usage_version::text) FROM iam_v2.entitlements WHERE id=$1`, r.room)

	id, ref := asstOpen(t, f, asstDefaults("ERASURE", r.macs[:1], r.anchor()))
	rv := asstReviewOf(t, f, id)
	if st, raw := asstAct(f, t, "export", id, rv, ref, rv.selectable()); st != 409 || !strings.Contains(raw, "wrong_purpose") {
		t.Errorf("a hand-over from an erasure request = %d %s", st, raw)
	}
	st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable())
	if st != 200 {
		t.Fatalf("erase = %d %s", st, raw)
	}
	var res struct {
		Ref     string         `json:"erasure_ref"`
		Counts  map[string]any `json:"counts"`
		Receipt map[string]any `json:"receipt"`
	}
	if err := json.Unmarshal([]byte(raw), &res); err != nil || res.Counts["sessions"] != float64(1) || res.Counts["devices"] != float64(1) {
		t.Fatalf("erase result: %v %s", err, raw)
	}
	if res.Receipt["reference"] != ref || res.Receipt["requester"] != "Ms R. Example" {
		t.Errorf("the request's own file must be handed back once: %v", res.Receipt)
	}

	// The claimed phone: address gone from the session and the device.
	if got := sessAddr(t, f, r.sess[0]); got != "" {
		t.Errorf("the claimed session kept its address: %s", got)
	}
	if got := devMAC(t, f, r.devs[0]); !strings.HasPrefix(got, "03:") {
		t.Errorf("the claimed device was kept: %s", got)
	}
	// The other occupants: untouched.
	for i := 1; i < 3; i++ {
		if got := devMAC(t, f, r.devs[i]); got != r.macs[i] {
			t.Errorf("occupant %d's device changed: %s", i, got)
		}
		if got := sessAddr(t, f, r.sess[i]); got != r.ips[i]+","+r.macs[i] {
			t.Errorf("occupant %d's session changed: %s", i, got)
		}
	}
	// The reservation, its holder, the room's access and its usage counters are untouched.
	if got := gdStr(t, f, `SELECT concat_ws(',',external_reservation_id LIKE 'anon-%', normalized_room_number) FROM iam_v2.stays WHERE id=$1`, r.stay); got != "f,101" {
		t.Errorf("the reservation changed: %s", got)
	}
	if got := gdStr(t, f, `SELECT concat_ws(',',first_name_norm,last_name_norm) FROM iam_v2.stay_guests WHERE stay_id=$1`, r.stay); got != "ada,lovelace" {
		t.Errorf("the holder changed: %s", got)
	}
	if got := gdStr(t, f, `SELECT concat_ws(',', status, consumed_data_bytes::text, consumed_online_seconds::text, usage_version::text) FROM iam_v2.entitlements WHERE id=$1`, r.room); got != consumedBefore {
		t.Errorf("the room's entitlement changed: %s -> %s", consumedBefore, got)
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM iam_v2.accounting_records WHERE session_id=ANY($1::uuid[])`, r.sess); got == "" {
		t.Error("accounting is unreadable")
	}
	// The activity log: the anonymised phone's address is replaced where it was searched for; another's is not.
	for _, sp := range spellings {
		if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND payload::text ILIKE '%'||$2||'%' AND action='guest_data.searched'`, f.tenant, sp); got != "0" {
			t.Errorf("the anonymised phone's address, spelled %q, is still in the activity log (%s entries)", sp, got)
		}
	}
	if got := gdStr(t, f, `SELECT count(*)::text FROM public.audit_log WHERE tenant_id=$1 AND payload::text ILIKE '%'||$2||'%'`, f.tenant, r.macs[1]); got != "1" {
		t.Errorf("another occupant's address in the activity log changed (%s entries)", got)
	}
	// The request: closed and scrubbed; the register lists it; the ids are kept, the addresses are not.
	got := asstStatus(t, f, id)
	if got["status"] != "COMPLETED" || got["addresses"] != nil || got["requester"] != "[removed]" {
		t.Errorf("a completed request keeps no address or text: %v", got)
	}
	if got := gdStr(t, f, `SELECT subject_kind FROM iam_v2.guest_data_erasures WHERE erasure_ref=$1`, res.Ref); got != "assisted" {
		t.Errorf("the erasure register: %s", got)
	}
	// The ids of what was anonymised stay (they identify nobody now); the anchors do not.
	if got := gdStr(t, f, `SELECT (SELECT count(*) FROM iam_v2.assisted_guest_request_records WHERE request_id=$1::uuid AND action='ANONYMISED')::text || ',' ||
		(SELECT count(*) FROM iam_v2.assisted_guest_request_anchors WHERE request_id=$1::uuid)::text`, id); got != "2,0" {
		t.Errorf("records and anchors after an erasure: %s, want 2,0", got)
	}
	// It cannot run twice.
	if st, raw := asstAct(f, t, "erase", id, rv, ref, rv.selectable()); st != 409 || !strings.Contains(raw, "closed") {
		t.Errorf("a second erasure from the same request = %d %s", st, raw)
	}
}
