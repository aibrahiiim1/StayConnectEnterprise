package main

import (
	"regexp"
	"strings"
	"testing"
)

// The portal serves hotels, cafés, restaurants and offices: only the strings of the PMS, room and post-stay
// features may speak of hotels, reception, rooms or stays. Everything else, in every language, must read for any site.
var hospitalityWords = regexp.MustCompile(`(?i)hotel|hôtel|hôtels|отел|гостиниц|فندق|فنادق|reception|réception|rezeption|ricevimento|ресепшен|стойк[аеи] регистрации|الاستقبال`)

func isHospitalityKey(key string) bool {
	for _, p := range []string{"pms.", "method.pms", "method.poststay", "poststay.", "help.pms", "help.poststay", "acq.room", "room.", "err.room.", "err.generic", "rep.", "tab.pms"} {
		if strings.HasPrefix(key, p) {
			return true
		}
	}
	return false
}

func TestOnlyHospitalityStringsSpeakOfHotels(t *testing.T) {
	for _, l := range portalLanguages {
		for key, text := range builtinStrings[l.Code] {
			if !isHospitalityKey(key) && hospitalityWords.MatchString(text) {
				t.Errorf("%s %s mentions a hotel or reception: %q", l.Code, key, text)
			}
		}
	}
}

// The default notice names the operator of the Wi-Fi as responsible, keeps what is recorded, and tells the client
// where to ask: it must say all of that in every language without assuming a reception desk.
func TestDefaultPrivacyNoticeReadsForAnySite(t *testing.T) {
	want := map[string][]string{
		"en": {"its operator", "records", "staff here"},
		"ar": {"الجهة المشغِّلة", "تسجّل", "العاملين هنا"},
		"de": {"Betreiber", "speichert", "Personal vor Ort"},
		"fr": {"exploitant", "enregistre", "personnel sur place"},
		"it": {"gestore", "registra", "personale"},
		"ru": {"оператор", "записывает", "персонала"},
	}
	for _, l := range portalLanguages {
		notice := builtinStrings[l.Code]["privacy.default"]
		if notice == "" {
			t.Errorf("%s has no default privacy notice", l.Code)
			continue
		}
		for _, w := range want[l.Code] {
			if !strings.Contains(notice, w) {
				t.Errorf("%s privacy.default no longer says %q: %q", l.Code, w, notice)
			}
		}
	}
	if !strings.Contains(landingHTML, `data-i18n-en="`+builtinStrings["en"]["privacy.default"]+`"`) {
		t.Error("the page's English privacy notice differs from the translation table's English")
	}
}
