package contracts

// This file enumerates every closed status/kind used across services. Each type
// is a string enum (stable JSON wire values) with a Valid method for boundary
// validation. Wire values are lower_snake and MUST NOT change once frozen.

// NodeStatus is the lifecycle state of a fleet node.
type NodeStatus string

const (
	NodeStatusProvisioning NodeStatus = "provisioning" // being created by the orchestrator
	NodeStatusActive       NodeStatus = "active"       // serving traffic
	NodeStatusDegraded     NodeStatus = "degraded"     // reachable but impaired (loss/throttle)
	NodeStatusBlocked      NodeStatus = "blocked"      // detected blocked from one or more regions
	NodeStatusDraining     NodeStatus = "draining"     // no new users; existing being migrated
	NodeStatusRetired      NodeStatus = "retired"      // torn down
)

// Valid reports whether s is a known NodeStatus.
func (s NodeStatus) Valid() bool {
	switch s {
	case NodeStatusProvisioning, NodeStatusActive, NodeStatusDegraded,
		NodeStatusBlocked, NodeStatusDraining, NodeStatusRetired:
		return true
	}
	return false
}

// NodeRole separates entry (mimicry, rotation) from exit so an attack on entry
// does not burn the exit. A single node may play both roles (combined).
type NodeRole string

const (
	NodeRoleEntry    NodeRole = "entry"
	NodeRoleExit     NodeRole = "exit"
	NodeRoleCombined NodeRole = "combined"
)

// Valid reports whether r is a known NodeRole.
func (r NodeRole) Valid() bool {
	switch r {
	case NodeRoleEntry, NodeRoleExit, NodeRoleCombined:
		return true
	}
	return false
}

// TransportType is the anti-censorship transport family. The fleet runs several
// independently at once (diversity over monoculture).
type TransportType string

const (
	TransportVlessReality    TransportType = "vless-reality"
	TransportAmneziaWG       TransportType = "amneziawg"
	TransportHysteria2       TransportType = "hysteria2"
	TransportShadowsocks2022 TransportType = "shadowsocks-2022"
)

// Valid reports whether t is a known TransportType.
func (t TransportType) Valid() bool {
	switch t {
	case TransportVlessReality, TransportAmneziaWG, TransportHysteria2, TransportShadowsocks2022:
		return true
	}
	return false
}

// TransportVersion tags the parameter schema revision of a transport variant, so
// clients and nodes can negotiate/skip shapes they do not understand. Bump when
// a variant's fields change in a non-additive way.
type TransportVersion string

const (
	TransportVersionV1 TransportVersion = "v1"
)

// Valid reports whether v is a known TransportVersion.
func (v TransportVersion) Valid() bool {
	return v == TransportVersionV1
}

// UserStatus is the account state.
type UserStatus string

const (
	UserStatusActive    UserStatus = "active"
	UserStatusSuspended UserStatus = "suspended" // e.g. abuse / fraud hold
	UserStatusExpired   UserStatus = "expired"   // subscription lapsed
	UserStatusBanned    UserStatus = "banned"
)

// Valid reports whether s is a known UserStatus.
func (s UserStatus) Valid() bool {
	switch s {
	case UserStatusActive, UserStatusSuspended, UserStatusExpired, UserStatusBanned:
		return true
	}
	return false
}

// SubscriptionStatus is the billing state of a subscription.
type SubscriptionStatus string

const (
	SubscriptionStatusTrialing SubscriptionStatus = "trialing"
	SubscriptionStatusActive   SubscriptionStatus = "active"
	SubscriptionStatusPastDue  SubscriptionStatus = "past_due"
	SubscriptionStatusCanceled SubscriptionStatus = "canceled"
	SubscriptionStatusExpired  SubscriptionStatus = "expired"
)

// Valid reports whether s is a known SubscriptionStatus.
func (s SubscriptionStatus) Valid() bool {
	switch s {
	case SubscriptionStatusTrialing, SubscriptionStatusActive, SubscriptionStatusPastDue,
		SubscriptionStatusCanceled, SubscriptionStatusExpired:
		return true
	}
	return false
}

// SubscriptionPlan is the pricing tier (see architecture.md economics).
type SubscriptionPlan string

const (
	SubscriptionPlanBasic     SubscriptionPlan = "basic"     // ~100 RUB: traffic/speed limited
	SubscriptionPlanUnlimited SubscriptionPlan = "unlimited" // ~200-300 RUB: no cap, more devices
)

// Valid reports whether p is a known SubscriptionPlan.
func (p SubscriptionPlan) Valid() bool {
	switch p {
	case SubscriptionPlanBasic, SubscriptionPlanUnlimited:
		return true
	}
	return false
}

// SignalType classifies a field observation from a client — the raw material of
// the "where did it get blocked" feedback loop.
type SignalType string

const (
	SignalOK            SignalType = "ok"             // healthy connection (baseline)
	SignalReset         SignalType = "reset"          // TCP RST / connection reset
	SignalTimeout       SignalType = "timeout"        // no response
	SignalDPIBlock      SignalType = "dpi_block"      // identified DPI drop
	SignalActiveProbe   SignalType = "probe"          // suspected active probing
	SignalThrottle      SignalType = "throttle"       // bandwidth throttled
	SignalDNSPoison     SignalType = "dns_poison"     // DNS tampering
	SignalHandshakeFail SignalType = "handshake_fail" // transport handshake failed
)

// Valid reports whether t is a known SignalType.
func (t SignalType) Valid() bool {
	switch t {
	case SignalOK, SignalReset, SignalTimeout, SignalDPIBlock, SignalActiveProbe,
		SignalThrottle, SignalDNSPoison, SignalHandshakeFail:
		return true
	}
	return false
}

// HealthStatus is the orchestrator's verdict on a node from active probing.
type HealthStatus string

const (
	HealthHealthy     HealthStatus = "healthy"
	HealthDegraded    HealthStatus = "degraded"
	HealthBlocked     HealthStatus = "blocked"
	HealthUnreachable HealthStatus = "unreachable"
)

// Valid reports whether s is a known HealthStatus.
func (s HealthStatus) Valid() bool {
	switch s {
	case HealthHealthy, HealthDegraded, HealthBlocked, HealthUnreachable:
		return true
	}
	return false
}

// DeliveryChannel is a subscription-delivery transport used to route around a
// blocked primary domain (decentralized config delivery).
type DeliveryChannel string

const (
	DeliveryHTTPS     DeliveryChannel = "https"
	DeliveryDoH       DeliveryChannel = "doh"
	DeliveryTelegram  DeliveryChannel = "telegram"
	DeliveryGitHubRaw DeliveryChannel = "github_raw"
	DeliveryDNSTXT    DeliveryChannel = "dns_txt"
)

// Valid reports whether c is a known DeliveryChannel.
func (c DeliveryChannel) Valid() bool {
	switch c {
	case DeliveryHTTPS, DeliveryDoH, DeliveryTelegram, DeliveryGitHubRaw, DeliveryDNSTXT:
		return true
	}
	return false
}

// Platform is the reporting client platform (coarse, for telemetry).
type Platform string

const (
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
	PlatformWindows Platform = "windows"
	PlatformMacOS   Platform = "macos"
	PlatformLinux   Platform = "linux"
)

// Valid reports whether p is a known Platform.
func (p Platform) Valid() bool {
	switch p {
	case PlatformAndroid, PlatformIOS, PlatformWindows, PlatformMacOS, PlatformLinux:
		return true
	}
	return false
}
