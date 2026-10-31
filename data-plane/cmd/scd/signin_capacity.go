package main

// The site's sign-in limits and capacity settings (migration 0162), read through iam_v2.signin_capacity_settings_get and
// kept for settingsCacheTTL (cleared at once by the database's notification, AD-52). A database that cannot answer yields
// the defaults, which are the previous fixed values: an unreadable setting never loosens a limit.

import (
	"context"
	"log/slog"
	"time"

	"github.com/stayconnect/enterprise/data-plane/internal/otp"
	"github.com/stayconnect/enterprise/data-plane/internal/poststay"
)

// signinCeilingFloor is the lowest appliance-wide sign-in ceiling (attempts a minute) whatever the licence or the setting says.
const signinCeilingFloor = 600

type signinCapacity struct {
	OTP                 otp.Limits
	CeilingPerMinute    int // 0 = follow the licence guest cap
	PauseExemptHours    int
	DeviceRetentionDays int
	ImageMaxEdgePx      int
	ImageQuality        int
	PostStaySigninDays  int             // RETIRED (migration 0193): read for the record only; the window is the After Check-out entitlement's own
	PostStay            poststay.Limits // post-stay brute-force bounds (migration 0190)
	FromDatabase        bool
}

func defaultSigninCapacity() signinCapacity {
	return signinCapacity{
		OTP:                 otp.Limits{}.WithDefaults(),
		PauseExemptHours:    24,
		DeviceRetentionDays: 90,
		ImageMaxEdgePx:      2560,
		ImageQuality:        82,
		PostStaySigninDays:  7,
		PostStay:            poststay.DefaultLimits(),
	}
}

func (s *server) signinCapacity(ctx context.Context) signinCapacity {
	if s.db == nil || s.tenID == "" || s.siteID == "" {
		return defaultSigninCapacity()
	}
	v, err := s.settings.get("signin-capacity", func() (any, error) {
		var c signinCapacity
		var lifetime, attempts, devCap, destCap, srcCap int
		var ceiling *int
		var version int64
		var at *time.Time
		var by *string
		cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		defer cancel()
		if err := s.db.QueryRow(cctx, `SELECT otp_code_lifetime_minutes, otp_max_attempts, otp_device_hourly_cap,
		       otp_destination_hourly_cap, otp_source_hourly_cap, signin_ceiling_per_minute, network_pause_exempt_hours,
		       device_retention_days, portal_image_max_edge_px, portal_image_quality, post_stay_signin_days,
		       post_stay_device_attempts_per_minute, post_stay_ip_attempts_per_minute, post_stay_wrong_answers_before_lockout,
		       post_stay_lockout_minutes, post_stay_new_device_room_attempts_per_minute, post_stay_appliance_attempts_per_minute,
		       config_version, updated_at, updated_by
		  FROM iam_v2.signin_capacity_settings_get($1::uuid,$2::uuid)`, s.tenID, s.siteID).
			Scan(&lifetime, &attempts, &devCap, &destCap, &srcCap, &ceiling, &c.PauseExemptHours,
				&c.DeviceRetentionDays, &c.ImageMaxEdgePx, &c.ImageQuality, &c.PostStaySigninDays,
				&c.PostStay.DeviceAttemptsPerMinute, &c.PostStay.IPAttemptsPerMinute, &c.PostStay.WrongAnswersBeforeLockout,
				&c.PostStay.LockoutMinutes, &c.PostStay.NewDeviceRoomAttemptsPerMinute, &c.PostStay.ApplianceAttemptsPerMinute,
				&version, &at, &by); err != nil {
			return nil, err
		}
		c.PostStay = c.PostStay.Bounded()
		c.OTP = otp.Limits{Lifetime: time.Duration(lifetime) * time.Minute, MaxAttempts: attempts,
			DeviceHourlyCap: devCap, DestinationHourlyCap: destCap, SourceHourlyCap: srcCap}.WithDefaults()
		if ceiling != nil {
			c.CeilingPerMinute = *ceiling
		}
		c.FromDatabase = true
		return c, nil
	})
	if err != nil {
		slog.Warn("sign-in capacity settings unreadable; using the defaults", "err", err)
		return defaultSigninCapacity()
	}
	return v.(signinCapacity)
}

// signinCeiling is the appliance-wide sign-in attempts a minute (AD-51). A configured value wins; otherwise one attempt
// per licensed concurrent guest every two minutes, and never below signinCeilingFloor, so a hotel whose licence allows
// 2,000 guests is not refused at 600 while an unlicensed or small appliance keeps the floor.
func (s *server) signinCeiling(ctx context.Context) int {
	return ceilingFor(s.signinCapacity(ctx).CeilingPerMinute, s.licensedGuestCap())
}

func ceilingFor(configured, licensedGuests int) int {
	if configured >= signinCeilingFloor {
		return configured
	}
	if byLicence := licensedGuests / 2; byLicence > signinCeilingFloor {
		return byLicence
	}
	return signinCeilingFloor
}

func (s *server) licensedGuestCap() int {
	if s.lic == nil {
		return 0
	}
	if n := s.lic.MaxConcurrentOnlineGuests(); n > 0 {
		return int(n)
	}
	return 0
}
