package main

// A PATCH FIELD THAT CAN BE CLEARED.
//
// The edit forms sent `undefined` for an emptied field and the handlers wrote COALESCE($n, column), so an
// operator who deleted an account's valid-until date, or a group's description, saved the form and watched the
// old value come back. A *T cannot tell "absent" (keep it) from "null" (clear it): both decode to nil.
//
// optional[T] can. encoding/json calls UnmarshalJSON for an explicit null as well as for a value, and never for
// a key that is absent, so:
//
//	absent      -> Set=false               keep the stored value
//	"k": null   -> Set=true,  Value=nil    clear it
//	"k": v      -> Set=true,  Value=&v     set it

import "encoding/json"

type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	if string(b) == "null" {
		o.Value = nil
		return nil
	}
	var v T
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// cleared reports an explicit null.
func (o optional[T]) cleared() bool { return o.Set && o.Value == nil }

// or returns the effective value against the stored one: the new value when set, nil when cleared, the stored
// value when absent.
func (o optional[T]) or(stored *T) *T {
	if !o.Set {
		return stored
	}
	return o.Value
}
