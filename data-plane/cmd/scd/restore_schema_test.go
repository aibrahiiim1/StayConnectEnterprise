package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// P9 M3: a restore compares the backup's schema with the installed software's, in both directions, before anything is replaced.
func TestRestoreSchemaCheck(t *testing.T) {
	live := []string{"column iam_v2.stays.id uuid NO", "column iam_v2.stays.vip boolean YES", "function iam_v2.run_data_retention(p_tenant uuid)"}
	cases := []struct {
		name    string
		backup  []string
		ok      bool
		mention string
	}{
		{"same", live, true, "is the one this appliance's software expects"},
		{"older (lacks a column and a function)", []string{"column iam_v2.stays.id uuid NO"}, false, "older software"},
		{"newer (has an object the software does not know)", append(append([]string{}, live...), "column iam_v2.stays.new_rule text YES"), false, "newer software"},
		{"neither (a column changed type)", []string{"column iam_v2.stays.id text NO", "column iam_v2.stays.vip boolean YES", "function iam_v2.run_data_retention(p_tenant uuid)"}, false, "either direction"},
	}
	for _, c := range cases {
		old := schemaInventory
		schemaInventory = func(_ context.Context, db string) ([]string, error) {
			if db == pgDatabase {
				return live, nil
			}
			return c.backup, nil
		}
		detail, ok := (&server{}).restoreSchemaCheck(context.Background(), "restore_x")
		schemaInventory = old
		if ok != c.ok || !strings.Contains(detail, c.mention) {
			t.Errorf("%s: ok=%v detail=%q", c.name, ok, detail)
		}
		if !ok && !strings.Contains(detail, "Nothing was replaced") && !strings.Contains(detail, "nothing was replaced") {
			t.Errorf("%s: a refusal must say nothing was replaced: %q", c.name, detail)
		}
	}
}

func TestRestoreSchemaCheckRefusesWhenEitherSchemaCannotBeRead(t *testing.T) {
	old := schemaInventory
	defer func() { schemaInventory = old }()
	for name, fn := range map[string]func(context.Context, string) ([]string, error){
		"live unreadable": func(_ context.Context, db string) ([]string, error) { return nil, errors.New("down") },
		"live empty":      func(_ context.Context, db string) ([]string, error) { return nil, nil },
		"backup unreadable": func(_ context.Context, db string) ([]string, error) {
			if db == pgDatabase {
				return []string{"column a.b.c text NO"}, nil
			}
			return nil, errors.New("down")
		},
	} {
		schemaInventory = fn
		if _, ok := (&server{}).restoreSchemaCheck(context.Background(), "restore_x"); ok {
			t.Errorf("%s: an unreadable schema was treated as a match", name)
		}
	}
}
