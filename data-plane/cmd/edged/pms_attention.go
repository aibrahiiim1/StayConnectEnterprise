package main

// pmsSupersededByRoster is true for a review event (alias e over iam_v2.pms_unanswered_review_events) that a LATER complete
// roster has already made moot: the interface published a roster that covered every room it expected (rooms_enumerated >=
// rooms_expected), reconciled it with APPLY and COMPLETED, after the event arrived. The mirror then holds the PMS's own
// current answer, so an old replayed departure or cancellation is history, not work: it drops out of the dashboard, the
// connection health and the callouts, and stays in the event list (Stay events) and the audit trail untouched. A roster
// that was partial, refused or older than the event supersedes nothing, so the event stays actionable. Nothing is written:
// the event clears by itself and would return if the evidence did not hold.
const pmsSupersededByRoster = `EXISTS (SELECT 1 FROM iam_v2.pms_roster_reconciliation_runs rr
	WHERE rr.pms_interface_id = e.pms_interface_id AND rr.mode = 'APPLY' AND rr.outcome = 'COMPLETED'
	  AND rr.rooms_expected > 0 AND rr.rooms_enumerated >= rr.rooms_expected
	  AND COALESCE(rr.boundary_at, rr.run_at) >= e.received_at)`
