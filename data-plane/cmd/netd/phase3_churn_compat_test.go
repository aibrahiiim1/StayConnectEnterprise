package main

import "time"

// endedMidPassOf reads the count of sessions whose access ended while their plan was in flight. It is a function of its own so the churn harness
// also compiles against the pass that predates the counter (the "before" measurement), where this file is replaced by a stub returning zero.
func endedMidPassOf(res shapingPlanResponse) int { return res.EndedMidPass }

// dbCallCap is the bound on one database call of a pass; zero where the pass has no bound (the "before" measurement).
func dbCallCap() time.Duration { return phase3DBCallTimeout }
