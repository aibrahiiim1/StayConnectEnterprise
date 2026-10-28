package payment

import (
	"reflect"
	"testing"
)

// Audit AD-44: the production Engine has no way to start a provider request or apply a provider outcome. If one of these
// methods comes back, the money path that grants without an amount check comes back with it.
func TestEngineHasNoExecuteOrNotificationPath(t *testing.T) {
	typ := reflect.TypeOf(&Engine{})
	for _, name := range []string{"Execute", "HandleProviderNotification", "CreateChargeIntent", "CreateRefundIntent", "ProviderRefusals"} {
		if _, ok := typ.MethodByName(name); ok {
			t.Fatalf("Engine.%s exists again; the dormant execute/notification path was removed (AD-44)", name)
		}
	}
}
