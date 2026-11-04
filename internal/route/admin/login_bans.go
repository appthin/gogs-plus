package admin

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cockroachdb/errors"

	"gogs.io/gogs/internal/context"
	"gogs.io/gogs/internal/loginlimit"
)

const (
	tmplLoginBans = "admin/login_bans"

	// maxBanDurationMinutes caps a manual ban at one year so a typo cannot
	// lock someone out indefinitely.
	maxBanDurationMinutes = 525600

	// maxBanAccountLength matches the limit the ban file reader applies, so an
	// over-long account is rejected instead of being stored in a form that can
	// never match a sign-in attempt.
	maxBanAccountLength = 254
)

type banView struct {
	Value     string
	Until     time.Time
	Remaining string
	Created   time.Time
	LastLogin string
	Reason    string
}

func LoginBans(c *context.Context) {
	c.Title("admin.login_bans")
	c.PageIs("Admin")
	c.PageIs("AdminLoginBans")

	limiter := loginlimit.Default()
	bans, err := limiter.ListBans(c.Req.Context())
	if err != nil {
		c.Error(err, "list login bans")
		return
	}

	now := time.Now()
	ipBans := make([]banView, 0, len(bans))
	accountBans := make([]banView, 0, len(bans))
	for _, ban := range bans {
		view := banView{
			Value:     ban.Value,
			Until:     ban.Until,
			Remaining: ban.Remaining(now).Round(time.Second).String(),
			Created:   ban.Created,
			LastLogin: ban.LastLogin,
			Reason:    ban.Reason,
		}
		switch ban.Kind {
		case loginlimit.KindIP:
			ipBans = append(ipBans, view)
		case loginlimit.KindAccount:
			accountBans = append(accountBans, view)
		}
	}

	c.Data["BanStatus"] = limiter.BanStatus()
	c.Data["IPBans"] = ipBans
	c.Data["AccountBans"] = accountBans
	c.Success(tmplLoginBans)
}

func LoginBanBan(c *context.Context) {
	kind := loginlimit.Kind(strings.TrimSpace(c.Query("kind")))
	value := strings.TrimSpace(c.Query("value"))

	minutes, err := parseBanDuration(c.Query("durationMinutes"))
	if err == nil {
		switch kind {
		case loginlimit.KindIP:
			value = loginlimit.NormalizeIP(value)
		case loginlimit.KindAccount:
			value = strings.ToLower(value)
		}
		err = validateBanTarget(kind, value)
	}

	if err == nil {
		now := time.Now()
		reason := strings.TrimSpace(c.Query("reason"))
		if reason == "" {
			reason = loginlimit.ReasonAddedByAdmin
		}
		err = loginlimit.Default().Ban(c.Req.Context(), loginlimit.Ban{
			Kind:    kind,
			Value:   value,
			Until:   now.Add(time.Duration(minutes) * time.Minute),
			Created: now,
			Reason:  reason,
		})
	}

	if err != nil {
		c.Flash.Error(c.Tr("admin.login_bans.ban_failed", err))
	} else {
		c.Flash.Success(c.Tr("admin.login_bans.ban_success"))
	}
	c.RedirectSubpath("/admin/login-bans")
}

func LoginBanUnban(c *context.Context) {
	kind := loginlimit.Kind(strings.TrimSpace(c.Query("kind")))
	value := strings.TrimSpace(c.Query("value"))

	err := validateBanTarget(kind, value)
	if err == nil {
		err = loginlimit.Default().Unban(c.Req.Context(), kind, value)
	}

	if err != nil {
		c.Flash.Error(c.Tr("admin.login_bans.unban_failed", err))
	} else {
		c.Flash.Success(c.Tr("admin.login_bans.unban_success"))
	}
	c.RedirectSubpath("/admin/login-bans")
}

func LoginBanReload(c *context.Context) {
	if err := loginlimit.Default().ReloadBans(c.Req.Context()); err != nil {
		c.Flash.Error(c.Tr("admin.login_bans.reload_failed", err))
	} else {
		c.Flash.Success(c.Tr("admin.login_bans.reload_success"))
	}
	c.RedirectSubpath("/admin/login-bans")
}

func validateBanTarget(kind loginlimit.Kind, value string) error {
	switch kind {
	case loginlimit.KindIP, loginlimit.KindAccount:
	default:
		return errors.Newf("unknown ban kind %q", kind)
	}
	if value == "" {
		return errors.New("ban value is empty")
	}
	if kind == loginlimit.KindAccount && utf8.RuneCountInString(value) > maxBanAccountLength {
		return errors.Newf("account name is longer than %d characters", maxBanAccountLength)
	}
	return nil
}

func parseBanDuration(raw string) (int, error) {
	minutes, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, errors.New("duration must be a whole number of minutes")
	}
	if minutes <= 0 || minutes > maxBanDurationMinutes {
		return 0, errors.Newf("duration must be between 1 and %d minutes", maxBanDurationMinutes)
	}
	return minutes, nil
}
