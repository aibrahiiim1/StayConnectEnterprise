package iamv2

import (
	"context"
	"errors"
	"testing"
)

// An Email account client is placed in a Client Group by its VERIFIED address (audit C-6): EMAIL_DOMAIN rules
// used to be evaluated for principal sign-ins only, so an Email account never matched a domain group.

type emailAccountTx struct {
	spyTx
	email    string
	emailErr error
	groups   []ClientGroup
	asked    int
}

func (t *emailAccountTx) AccountVerifiedEmail(context.Context, string, string, string) (string, error) {
	t.asked++
	return t.email, t.emailErr
}

func (t *emailAccountTx) LoadClientGroups(context.Context, string, string) ([]ClientGroup, error) {
	return t.groups, nil
}

func partnerGroups() []ClientGroup {
	return []ClientGroup{{ID: "g-partner", Name: "Partners", Priority: 1, Enabled: true, Rules: []ClientGroupRule{
		{ID: "r1", Type: GroupRuleEmailDomain, Value: map[string]any{"domains": []any{"partner.example"}}},
	}}}
}

func TestAccountGroupPin_VerifiedEmailAccountMatchesItsDomainGroup(t *testing.T) {
	tx := &emailAccountTx{email: "someone@partner.example", groups: partnerGroups()}
	pin, err := accountGroupPin(context.Background(), tx, Request{TenantID: "t", SiteID: "s"}, "acct")
	if err != nil {
		t.Fatal(err)
	}
	if pin.id != "g-partner" || pin.name != "Partners" || pin.evidence["rule_type"] != GroupRuleEmailDomain {
		t.Fatalf("pin = %+v, want the Partners group decided by its EMAIL_DOMAIN rule", pin)
	}
}

func TestAccountGroupPin_NoProvenAddressDecidesNothing(t *testing.T) {
	// "" is what the repository answers for a staff-created account and for an unverified registration.
	tx := &emailAccountTx{email: "", groups: partnerGroups()}
	pin, err := accountGroupPin(context.Background(), tx, Request{}, "acct")
	if err != nil || pin.id != "" {
		t.Fatalf("pin = %+v err=%v, want no group", pin, err)
	}
}

func TestAccountGroupPin_OtherDomainIsPublic(t *testing.T) {
	tx := &emailAccountTx{email: "someone@elsewhere.example", groups: partnerGroups()}
	pin, err := accountGroupPin(context.Background(), tx, Request{}, "acct")
	if err != nil || pin.id != "" {
		t.Fatalf("pin = %+v err=%v, want no group", pin, err)
	}
}

func TestAccountGroupPin_LookupFailureIsAnErrorNotPublic(t *testing.T) {
	tx := &emailAccountTx{emailErr: errors.New("db down"), groups: partnerGroups()}
	if _, err := accountGroupPin(context.Background(), tx, Request{}, "acct"); err == nil {
		t.Fatal("a failed lookup must fail closed, not place the client in Public")
	}
}

func TestAccountGroupPin_RepositoryWithoutTheCapabilityDecidesNothing(t *testing.T) {
	pin, err := accountGroupPin(context.Background(), spyTx{}, Request{}, "acct")
	if err != nil || pin.id != "" {
		t.Fatalf("pin = %+v err=%v", pin, err)
	}
}
