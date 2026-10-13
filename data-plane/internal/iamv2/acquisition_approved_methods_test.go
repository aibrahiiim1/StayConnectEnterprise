package iamv2

import "testing"

// ONLY APPROVED ACQUISITION METHODS ARE EVER OFFERED. The approved set is Free (NOT_REQUIRED), Voucher
// (PREPAID), Card (ONLINE_PAYMENT) and Room charge (PMS_POSTING); Room charge is offered only on the Room
// sign-in path, never here. MANUAL_APPROVAL stays dormant in the settlement contract and is never offered;
// CASH exists nowhere. The database refuses an unapproved method on a revision (TestAcquisitionDatabaseRules);
// this proves that even a revision that somehow listed one could not get it offered, for every subject kind,
// price and site switch.
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
