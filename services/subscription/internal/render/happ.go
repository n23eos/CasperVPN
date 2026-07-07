package render

import (
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"

	"github.com/caspervpn/contracts"
)

// HappMeta are the values rendered into Happ subscription headers. All are
// config- or account-derived; nothing about branding or domains is hardcoded.
type HappMeta struct {
	ProfileUpdateHours int    // Profile-Update-Interval (hours) — drives auto-refresh
	ProfileTitle       string // shown as the subscription name (base64-encoded on the wire)
	Announce           string // announcement banner (base64-encoded on the wire)
	SupportURL         string // support button
	WebPageURL         string // profile web page button

	UsedBytes  uint64 // download counter
	TotalBytes uint64 // 0 = unlimited
	ExpireUnix int64  // 0 = no expiry
}

// WriteHappHeaders sets the Happ / sing-box subscription headers on h. These
// headers make external clients self-update, so blocked nodes wash out quickly.
func WriteHappHeaders(h http.Header, m HappMeta) {
	if m.ProfileUpdateHours > 0 {
		h.Set("Profile-Update-Interval", strconv.Itoa(m.ProfileUpdateHours))
	}
	h.Set("Subscription-Userinfo", userinfo(m.UsedBytes, m.TotalBytes, m.ExpireUnix))
	// Routing profiles are carried in the config bodies; enable client routing.
	h.Set("Routing-Enable", "1")

	if m.ProfileTitle != "" {
		h.Set("Profile-Title", "base64:"+b64(m.ProfileTitle))
	}
	if m.Announce != "" {
		h.Set("Announce", "base64:"+b64(m.Announce))
	}
	if m.SupportURL != "" {
		h.Set("Support-Url", m.SupportURL)
	}
	if m.WebPageURL != "" {
		h.Set("Profile-Web-Page-Url", m.WebPageURL)
	}
}

// userinfo builds the SIP008-style "Subscription-Userinfo" value. upload is
// reported as 0 (we account combined traffic under download).
func userinfo(used, total uint64, expireUnix int64) string {
	parts := []string{
		"upload=0",
		"download=" + strconv.FormatUint(used, 10),
		"total=" + strconv.FormatUint(total, 10),
	}
	if expireUnix > 0 {
		parts = append(parts, "expire="+strconv.FormatInt(expireUnix, 10))
	}
	return strings.Join(parts, "; ")
}

// HappMetaFor derives header values from the subscription/user, layered over the
// operator-supplied UI strings.
func HappMetaFor(sub contracts.Subscription, user contracts.User, updateHours int, title, announce, supportURL, webURL string) HappMeta {
	var expire int64
	if sub.ExpiresAt != nil {
		expire = sub.ExpiresAt.Unix()
	}
	return HappMeta{
		ProfileUpdateHours: updateHours,
		ProfileTitle:       title,
		Announce:           announce,
		SupportURL:         supportURL,
		WebPageURL:         webURL,
		UsedBytes:          user.UsedBytes,
		TotalBytes:         sub.TrafficLimitBytes,
		ExpireUnix:         expire,
	}
}

// HappAddDeepLink builds the `happ://add/<base64>` deep link for a subscription
// URL. The subscriptionURL is whatever channel served the request (never a
// hardcoded domain) — Happ imports it as a standard subscription.
func HappAddDeepLink(subscriptionURL string) string {
	return "happ://add/" + b64(subscriptionURL)
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
