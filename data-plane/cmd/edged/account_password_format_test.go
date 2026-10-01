package main

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The per-style minimums are not free choices: each is ceil(40 / log2(alphabet size)). Holding the constants
// to the formula means a changed alphabet cannot quietly leave a style below the floor.
func TestPasswordStyleMinimumsMeetTheEntropyFloor(t *testing.T) {
	want := map[string]int{"mixed": 7, "upper_digits": 9, "lower_digits": 8, "digits": 14}
	if len(passwordStyles) != len(want) {
		t.Fatalf("%d styles, want %d", len(passwordStyles), len(want))
	}
	for _, p := range passwordStyles {
		computed := int(math.Ceil(passwordEntropyFloorBits / math.Log2(float64(len(p.Alphabet)))))
		if p.MinLength != computed {
			t.Errorf("%s: MinLength %d, formula gives %d", p.Key, p.MinLength, computed)
		}
		if p.MinLength != want[p.Key] {
			t.Errorf("%s: MinLength %d, the migration and the UI were written for %d", p.Key, p.MinLength, want[p.Key])
		}
		if p.entropyBits(p.MinLength) < passwordEntropyFloorBits {
			t.Errorf("%s at its minimum is %.2f bits, below the floor", p.Key, p.entropyBits(p.MinLength))
		}
		if p.MinLength > 1 && p.entropyBits(p.MinLength-1) >= passwordEntropyFloorBits {
			t.Errorf("%s: the minimum is not tight; %d would already meet the floor", p.Key, p.MinLength-1)
		}
	}
}

func TestPasswordAlphabetsExcludeAmbiguousCharacters(t *testing.T) {
	sizes := map[string]int{"mixed": 55, "upper_digits": 31, "lower_digits": 32, "digits": 8}
	for _, p := range passwordStyles {
		if strings.ContainsAny(p.Alphabet, "0O1Il") || strings.ContainsRune(p.Alphabet, 'o') {
			t.Errorf("%s alphabet %q contains an easily misread character", p.Key, p.Alphabet)
		}
		if len(p.Alphabet) != sizes[p.Key] {
			t.Errorf("%s alphabet has %d symbols, want %d", p.Key, len(p.Alphabet), sizes[p.Key])
		}
		seen := map[rune]bool{}
		for _, r := range p.Alphabet {
			if seen[r] {
				t.Errorf("%s alphabet repeats %q, which would bias the draw", p.Key, r)
			}
			seen[r] = true
		}
	}
	// The default is exactly what the hardcoded generator used before 0104.
	mixed, _ := lookupPasswordStyle(defaultPasswordStyle)
	if mixed.Alphabet != "ABCDEFGHJKMNPQRSTUVWXYZabcdefghijkmnpqrstuvwxyz23456789" || defaultPasswordLength != 14 {
		t.Errorf("default changed: %q / %d", mixed.Alphabet, defaultPasswordLength)
	}
}

func TestValidatePasswordFormatEnforcesTheFloorAndCeiling(t *testing.T) {
	cases := []struct {
		style  string
		length int
		ok     bool
	}{
		{"mixed", 6, false}, {"mixed", 7, true}, {"mixed", 14, true}, {"mixed", 32, true}, {"mixed", 33, false},
		{"upper_digits", 8, false}, {"upper_digits", 9, true},
		{"lower_digits", 7, false}, {"lower_digits", 8, true},
		{"digits", 8, false}, {"digits", 13, false}, {"digits", 14, true}, {"digits", 32, true},
		{"numbers", 14, false}, {"", 14, false}, {"MIXED", 14, false},
	}
	for _, c := range cases {
		if _, _, ok := validatePasswordFormat(c.style, c.length); ok != c.ok {
			t.Errorf("validate(%q, %d) = %v, want %v", c.style, c.length, ok, c.ok)
		}
		_, err := generatePasswordWith(c.style, c.length)
		if (err == nil) != c.ok {
			t.Errorf("generate(%q, %d) err=%v, want ok=%v -- the generator must refuse what the setter refuses",
				c.style, c.length, err, c.ok)
		}
	}
}

func TestGeneratedPasswordsMatchStyleAndLength(t *testing.T) {
	for _, p := range passwordStyles {
		re := regexp.MustCompile("^[" + regexp.QuoteMeta(p.Alphabet) + "]+$")
		for _, n := range []int{p.MinLength, 20, maxGeneratedPasswordLen} {
			seen := map[string]bool{}
			for i := 0; i < 50; i++ {
				pw, err := generatePasswordWith(p.Key, n)
				if err != nil {
					t.Fatalf("%s/%d: %v", p.Key, n, err)
				}
				if len(pw) != n {
					t.Fatalf("%s: length %d, want %d", p.Key, len(pw), n)
				}
				if !re.MatchString(pw) {
					t.Fatalf("%s: %q has a character outside the alphabet", p.Key, pw)
				}
				if ok, msg := validPassword(pw); !ok {
					t.Fatalf("%s: a generated password fails the operator rules: %s", p.Key, msg)
				}
				seen[pw] = true
			}
			if len(seen) < 49 {
				t.Errorf("%s/%d: only %d distinct passwords in 50 draws", p.Key, n, len(seen))
			}
		}
	}
}

// The bounds the UI receives are the bounds the server enforces.
func TestPasswordFormatLimitsAreTheEnforcedBounds(t *testing.T) {
	l := passwordFormatLimits()
	if l.EntropyFloorBits != 40 || l.MaxLength != 32 || l.DefaultStyle != "mixed" || l.DefaultLength != 14 {
		t.Fatalf("limits = %+v", l)
	}
	if len(l.Styles) != len(passwordStyles) {
		t.Fatalf("%d styles sent, %d enforced", len(l.Styles), len(passwordStyles))
	}
	for i, s := range l.Styles {
		if s.Key != passwordStyles[i].Key || s.MinLength != passwordStyles[i].MinLength || s.MaxLength != 32 ||
			s.Alphabet != passwordStyles[i].Alphabet {
			t.Errorf("style %d sent as %+v", i, s)
		}
	}
}

// The SQL setter and the table CHECK carry the same per-style minimums as Go. Read from the migration itself,
// so the three copies cannot drift apart without a test noticing.
func TestMigrationCarriesTheSameMinimums(t *testing.T) {
	raw, err := os.ReadFile("../../migrations/0104_the_property_decides_what_a_generated_account_password_looks_like.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	src := string(raw)
	for _, p := range passwordStyles {
		re := regexp.MustCompile(`WHEN '` + p.Key + `'\s+THEN (\d+)`)
		m := re.FindAllStringSubmatch(src, -1)
		if len(m) != 2 {
			t.Fatalf("%s: expected the minimum in the CHECK and the setter, found %d", p.Key, len(m))
		}
		for _, g := range m {
			if n, _ := strconv.Atoi(g[1]); n != p.MinLength {
				t.Errorf("%s: migration says %d, Go says %d", p.Key, n, p.MinLength)
			}
		}
	}
	if !strings.Contains(src, "password_length <= 32") || !strings.Contains(src, "p_length > 32") {
		t.Error("the migration does not cap the length at 32")
	}
	if !strings.Contains(src, "COALESCE(s.password_style, 'mixed')") || !strings.Contains(src, "COALESCE(s.password_length, 14)") {
		t.Error("the reader's defaults are not mixed/14")
	}
}

// The account-password-settings key exists for the roles that hold voucher-code-settings, with the same power.
func TestAccountPasswordSettingsFollowsVoucherCodeSettings(t *testing.T) {
	for role := range rolePerms {
		for _, want := range []perm{permRead, permWrite} {
			if permFor([]string{role}, "account-password-settings", want) != permFor([]string{role}, "voucher-code-settings", want) {
				t.Errorf("%s: account-password-settings and voucher-code-settings differ at %v", role, want)
			}
		}
	}
	if permFor([]string{"front_office_operator"}, "account-password-settings", permWrite) {
		t.Error("the desk can change how strong every generated password is")
	}
}
