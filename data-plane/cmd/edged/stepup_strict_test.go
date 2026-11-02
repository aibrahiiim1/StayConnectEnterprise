package main

import (
	"context"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// strictStepUpHandlers re-verify the operator's password however the console confirms: keys and secrets, money,
// personal data, certificates and trust, PMS configuration, operator management, and the appliance's own network.
// Ordinary settings use the confirm-only reauth. Moving a handler off this list is a security decision.
var strictStepUpHandlers = []string{
	"changeOwnPassword", "setupOwnMFA", "disableOwnMFA", "newOwnRecoveryCodes", "resetEdgeOperatorMFA",
	"backupDownloadTicket", "restoreDatabaseBackup", "backupSettingsSet", "readLedgerBody",
	"addBackupKeyHolder", "removeBackupKeyHolder",
	"guestDataAction", "assistedAction",
	"savePaymentAccount", "savePaymentDomains", "putCardSettings", "setPaymentAccountConversion",
	"setFinancialMirrorMaxAge", "recordAnswerConfirmation", "stayAdminBlock",
	"putCardRecheckSettings", "resolveCardCase", "resolveRecoveryHold", "releaseRecovery",
	"authorizeZeroAttemptRetry", "requeueParkedCharge", "putPostingLaneSettings",
	"hotelAdminCertRotate", "portalHTTPSPutAddress", "portalHTTPSPutDomain", "portalHTTPSDeleteDomain", "portalHTTPSRenew",
	"rotatePMSInterfaceSecret", "publishPMSInterfaceRevision", "setPMSInterfaceLifecycle", "removePMSInterface",
	"testPMSConnection",
	"createNotifyProvider", "patchNotifyProvider", "createSocialProvider", "patchSocialProvider", "stepUpFromBody",
	"getSignInAttemptCredentials", "revealCode", "voucherCodeStepUp", "rotateVoucherKeyGeneration",
	"confirmYourPassword", "executeStayTransfer", "revokePostStayProfile", "sysNetApply", "sysNetRollback",
	"postReviewAction", "decideReviewEvent", "putReviewExpirySettings", "updateReconciliationSettings", "requestFullResync",
	"publishServicePlan", "publishCommercialPackage", "setCommercialPackageActive", "setGraceConfig", "enforcementStepUp",
	"runDatabaseBackup", "setGuestDeviceSetting",
}

func TestSensitiveHandlersVerifyThePasswordServerSide(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	bodies := map[string]string{}
	funcRe := regexp.MustCompile(`(?m)^func (?:\([^)]*\) )?(\w+)\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		locs := funcRe.FindAllStringSubmatchIndex(src, -1)
		for i, l := range locs {
			end := len(src)
			if i+1 < len(locs) {
				end = locs[i+1][0]
			}
			bodies[src[l[2]:l[3]]] = src[l[0]:end]
		}
	}
	lenient := regexp.MustCompile(`s\.(reauth|stepUp|stepUpActor)\(`)
	strict := regexp.MustCompile(`s\.(reauthStrict|stepUpStrict|stepUpActorStrict)\(`)
	for _, h := range strictStepUpHandlers {
		body, ok := bodies[h]
		if !ok {
			t.Errorf("%s: handler not found; update the list if it was renamed", h)
			continue
		}
		if lenient.MatchString(body) {
			t.Errorf("%s uses the confirm-only step-up; it must verify the password (reauthStrict / stepUpStrict / stepUpActorStrict)", h)
		}
		if !strict.MatchString(body) {
			t.Errorf("%s does not verify the password at all", h)
		}
	}
}

// The confirm-only step-up still verifies a password that is supplied, and the strict one never accepts an empty one.
func TestStrictStepUpRefusesAnEmptyPassword(t *testing.T) {
	s := &server{}
	r := httptest.NewRequest("POST", "/", nil)
	r = r.WithContext(context.WithValue(r.Context(), sessKey, &session{OperatorID: "op"}))
	if s.reauthStrict(r, "") {
		t.Fatal("strict step-up must refuse an empty password")
	}
	if !s.reauth(r, "") {
		t.Fatal("the confirm-only step-up accepts a signed-in session without a password")
	}
	if s.reauth(httptest.NewRequest("POST", "/", nil), "") {
		t.Fatal("without a session nothing passes")
	}
}

func TestTrunkDefaultPoolFollowsTheSubnetSize(t *testing.T) {
	for cidr, want := range map[string][2]string{
		"10.30.0.0/19":  {"10.30.0.10", "10.30.31.250"},
		"10.30.64.0/19": {"10.30.64.10", "10.30.95.250"},
		"10.30.0.0/20":  {"10.30.0.10", "10.30.15.250"},
		"10.30.5.0/24":  {"10.30.5.100", "10.30.5.250"},
	} {
		_, n, _ := net.ParseCIDR(cidr)
		a, b, ok := defaultPoolFor(n)
		if !ok || a != want[0] || b != want[1] {
			t.Errorf("%s: %s-%s %v, want %s-%s", cidr, a, b, ok, want[0], want[1])
		}
	}
	_, wide, _ := net.ParseCIDR("10.0.0.0/16")
	if _, _, ok := defaultPoolFor(wide); ok {
		t.Error("a /16 gets no automatic pool")
	}
}
