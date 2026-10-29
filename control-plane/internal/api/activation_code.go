package api

import (
	"crypto/ed25519"
	"encoding/base64"
	"net/http"

	"github.com/stayconnect/enterprise/control-plane/internal/applianceauth"
)

// activationCodeRefusal is the AD-1 check: the typed code must equal the code derived from the identity key Central
// holds for the waiting appliance. nil means the code is right. A missing code and a wrong code are different answers
// so the console can tell the operator what to do; neither reveals the expected code.
func activationCodeRefusal(publicKeyB64, typed string) *moveRefusal {
	if applianceauth.NormalizeActivationCode(typed) == "" {
		return &moveRefusal{http.StatusBadRequest, "activation_code_required",
			"Enter the activation code shown on the appliance (Admin Console, System, Appliance and licence)."}
	}
	raw, err := base64.RawStdEncoding.DecodeString(publicKeyB64)
	if err != nil || len(raw) != ed25519.PublicKeySize || !applianceauth.ActivationCodeMatches(ed25519.PublicKey(raw), typed) {
		return &moveRefusal{http.StatusConflict, "activation_code_mismatch",
			"The activation code does not match this waiting appliance. Read it again from the appliance's own screen; " +
				"if it still differs, the row may have been registered by another device: delete it and let the real appliance register."}
	}
	return nil
}
