package iamv2

import (
	"strconv"
	"strings"
)

const (
	EnvMaster     = "STAYCONNECT_IAMV2_MASTER"
	EnvVoucher    = "STAYCONNECT_IAMV2_VOUCHER"
	EnvAccount    = "STAYCONNECT_IAMV2_ACCOUNT"
	EnvOTP        = "STAYCONNECT_IAMV2_OTP"
	EnvSocial     = "STAYCONNECT_IAMV2_SOCIAL"
	EnvSocialStub = "STAYCONNECT_IAMV2_ALLOW_SOCIAL_STUB"
)

type Getenv func(string) string

// parseBoolStrict errors on a malformed value rather than silently defaulting a typo to ON or OFF.
func parseBoolStrict(name, v string) (bool, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return false, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, &Error{Code: ErrConfig, Msg: "malformed boolean for " + name}
	}
	return b, nil
}

// LoadConfigFromEnv defaults everything OFF; a malformed boolean or a method enabled while master is off is a
// startup failure. The production profile never allows the social Stub.
func LoadConfigFromEnv(get Getenv, productionProfile bool) (Config, error) {
	master, err := parseBoolStrict(EnvMaster, get(EnvMaster))
	if err != nil {
		return Config{}, err
	}
	methods := map[Method]bool{}
	for _, mv := range []struct {
		m   Method
		env string
	}{{MethodVoucher, EnvVoucher}, {MethodAccount, EnvAccount}, {MethodOTP, EnvOTP}, {MethodSocial, EnvSocial}} {
		on, err := parseBoolStrict(mv.env, get(mv.env))
		if err != nil {
			return Config{}, err
		}
		methods[mv.m] = on
	}
	stub, err := parseBoolStrict(EnvSocialStub, get(EnvSocialStub))
	if err != nil {
		return Config{}, err
	}
	if productionProfile {
		stub = false
	}
	cfg := Config{MasterEnabled: master, Methods: methods, AllowSocialStub: stub}
	// The guest IAM authority is locked on a production BUILD (selecting the superseded path is a startup
	// failure), applied before Validate. Keyed on productionBuild, not productionProfile: both services pass
	// productionProfile=true even on the development appliance, so the binary alone decides.
	if err := enforceGuestAuthorityLock(&cfg, productionBuild, get); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) SafeFlagSummary() string {
	var b strings.Builder
	b.WriteString("iamv2 master=")
	b.WriteString(strconv.FormatBool(c.MasterEnabled))
	for _, m := range []Method{MethodVoucher, MethodAccount, MethodOTP, MethodSocial} {
		b.WriteString(" ")
		b.WriteString(string(m))
		b.WriteString("=")
		b.WriteString(strconv.FormatBool(c.Methods[m]))
	}
	b.WriteString(" social_stub=")
	b.WriteString(strconv.FormatBool(c.AllowSocialStub))
	// The build profile, not the flags, decides whether the guest authority is locked.
	b.WriteString(" build=")
	b.WriteString(BuildProfile())
	return b.String()
}
