package main

// optional[T] is a PATCH field that can be cleared: a *T cannot tell "absent" from "null" (both nil), but
// encoding/json calls UnmarshalJSON for an explicit null and never for an absent key, so:
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

func (o optional[T]) cleared() bool { return o.Set && o.Value == nil }

func (o optional[T]) or(stored *T) *T {
	if !o.Set {
		return stored
	}
	return o.Value
}
