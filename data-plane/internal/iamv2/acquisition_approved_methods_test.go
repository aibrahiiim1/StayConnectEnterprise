package iamv2

import "testing"

// Even a revision that somehow listed an unapproved method (MANUAL_APPROVAL, CASH) must never get it offered;
// Room charge is offered only on the Room sign-in path, never here.
func TestOnlyApprovedMethodsAreEverOffered(t *testing.T) {
	approved := map[string]bool{AcquireFree: true, AcquireVoucher: true, AcquireCard: true}
	listed := []string{AcquireFree, AcquireVoucher, AcquireCard, AcquireRoomCharge, "MANUAL_APPROVAL", "CASH", "", "card"}
	kinds := []CommerceSubject{
		{Kind: SubjectPrincipal, PrincipalID: "p"}, {Kind: SubjectAccount}, {Kind: SubjectVoucher, VoucherID: "v"}, {Kind: SubjectAnonymous},
	}
	for _, price := range []int64{0, 100} {
		for _, subj := range kinds {
			for _, paid := range []bool{false, true} {
				for _, card := range []bool{false, true} {
					for _, room := range []bool{false, true} {
						pkg := PackageRevisionRow{PriceMinor: price, Currency: "USD", CurrencyExponent: 2, SettlementMethods: listed}
						site := SiteMethods{PaidAccess: paid, Card: card, RoomCharge: room, CardCurrencies: []string{"USD"}}
						for _, m := range applicableMethods(pkg, AuthContextRow{Subject: subj}, site) {
							if !approved[m] {
								t.Errorf("price %d subject %s paid=%v card=%v room=%v: offered %q", price, subj.Kind, paid, card, room, m)
							}
						}
					}
				}
			}
		}
	}
}
