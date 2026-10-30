package poststay

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"
)

func TestLimitsZeroValueIsTheDefaultsNeverUnlimited(t *testing.T) {
	d := Limits{}.Bounded()
	want := Limits{DeviceAttemptsPerMinute: 10, IPAttemptsPerMinute: 20, WrongAnswersBeforeLockout: 5, LockoutMinutes: 15,
		NewDeviceRoomAttemptsPerMinute: 10, ApplianceAttemptsPerMinute: 200}
	if d != want || DefaultLimits() != want {
		t.Fatalf("defaults %+v, want the former constants %+v", d, want)
	}
}

func TestLimitsOutOfRangeFallBackToTheDefaultPerField(t *testing.T) {
	in := Limits{DeviceAttemptsPerMinute: 2, IPAttemptsPerMinute: 201, WrongAnswersBeforeLockout: 7, LockoutMinutes: 241,
		NewDeviceRoomAttemptsPerMinute: -1, ApplianceAttemptsPerMinute: 5000}
	got := in.Bounded()
	if got.DeviceAttemptsPerMinute != 10 || got.IPAttemptsPerMinute != 20 || got.WrongAnswersBeforeLockout != 7 ||
		got.LockoutMinutes != 15 || got.NewDeviceRoomAttemptsPerMinute != 10 || got.ApplianceAttemptsPerMinute != 5000 {
		t.Fatalf("got %+v", got)
	}
	// Bounds are inclusive.
	edge := Limits{DeviceAttemptsPerMinute: 3, IPAttemptsPerMinute: 200, WrongAnswersBeforeLockout: 3, LockoutMinutes: 240,
		NewDeviceRoomAttemptsPerMinute: 60, ApplianceAttemptsPerMinute: 20}
	if edge.Bounded() != edge {
		t.Fatalf("an edge value was replaced: %+v", edge.Bounded())
	}
}

// The code's bounds and defaults are the database's CHECKs and column defaults (migration 0190): one source of truth,
// pinned so neither can drift alone.
func TestLimitBoundsEqualMigration0190(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("..", "..", "migrations", "0190_*.up.sql"))
	if len(files) != 1 {
		t.Fatalf("migration 0190: %v", files)
	}
	raw, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatal(err)
	}
	sql := string(raw)
	for col, b := range map[string]limitBound{
		"post_stay_device_attempts_per_minute":          BoundDeviceAttempts,
		"post_stay_ip_attempts_per_minute":              BoundIPAttempts,
		"post_stay_wrong_answers_before_lockout":        BoundWrongAnswers,
		"post_stay_lockout_minutes":                     BoundLockoutMinutes,
		"post_stay_new_device_room_attempts_per_minute": BoundNewDeviceRoom,
		"post_stay_appliance_attempts_per_minute":       BoundApplianceAttempts,
	} {
		check := regexp.MustCompile(`CHECK \(` + col + ` BETWEEN (\d+) AND (\d+)\)`).FindStringSubmatch(sql)
		def := regexp.MustCompile(col + ` integer NOT NULL DEFAULT (\d+)`).FindStringSubmatch(sql)
		if check == nil || def == nil {
			t.Fatalf("%s: no CHECK or DEFAULT in the migration", col)
		}
		lo, _ := strconv.Atoi(check[1])
		hi, _ := strconv.Atoi(check[2])
		d, _ := strconv.Atoi(def[1])
		if lo != b.min || hi != b.max || d != b.def {
			t.Errorf("%s: migration %d..%d default %d, code %d..%d default %d", col, lo, hi, d, b.min, b.max, b.def)
		}
	}
	// A lockout is never permanent: the largest the database accepts is four hours.
	if BoundLockoutMinutes.max > 240 {
		t.Fatal("lockout may exceed four hours")
	}
}
