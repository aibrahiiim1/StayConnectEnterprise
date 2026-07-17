package iamv2

import (
	"strconv"
	"strings"
)

// Env variable names (deployment-controlled; no DB flag table, no admin UI).
const (
	EnvMaster     = "STAYCONNECT_IAMV2_MASTER"
	EnvVoucher    = "STAYCONNECT_IAMV2_VOUCHER"
	EnvAccount    = "STAYCONNECT_IAMV2_ACCOUNT"
	EnvOTP        = "STAYCONNECT_IAMV2_OTP"
	EnvSocial     = "STAYCONNECT_IAMV2_SOCIAL"
	EnvSocialStub = "STAYCONNECT_IAMV2_ALLOW_SOCIAL_STUB"
)

// Getenv is the environment accessor (injectable for tests).
type Getenv func(string) string

// parseBoolStrict returns false for an absent/empty value, and errors on a malformed value (fail
// closed at startup rather than silently defaulting a typo to ON or OFF).
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

// LoadConfigFromEnv builds a Config from environment variables. Everything defaults OFF; a malformed
// boolean or a method-enabled-while-master-off combination is a startup failure (Validate). The
// production profile must never allow the social Stub.
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
		// Production permanently refuses the social Stub regardless of the flag.
		stub = false
	}
	cfg := Config{MasterEnabled: master, Methods: methods, AllowSocialStub: stub}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// SafeFlagSummary returns a log-safe description of the flag states (no secrets).
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
	return b.String()
}
