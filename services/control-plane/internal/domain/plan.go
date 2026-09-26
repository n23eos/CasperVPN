package domain

import "github.com/caspervpn/contracts"

// ApplyPlanLimits is shared by creation and atomic versioned plan changes.
func ApplyPlanLimits(sub *contracts.Subscription, plan contracts.SubscriptionPlan) {
	sub.Plan = plan
	switch plan {
	case contracts.SubscriptionPlanBasic:
		sub.TrafficLimitBytes = 100 * 1024 * 1024 * 1024
		sub.SpeedLimitMbps = 50
		sub.DeviceLimit = 2
	case contracts.SubscriptionPlanUnlimited:
		sub.TrafficLimitBytes = 0
		sub.SpeedLimitMbps = 0
		sub.DeviceLimit = 5
	}
}
