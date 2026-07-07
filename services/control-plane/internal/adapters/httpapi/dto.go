package httpapi

import (
	"time"

	"github.com/caspervpn/contracts"
	"github.com/caspervpn/control-plane/internal/domain"
)

// createUserRequest is the POST /v1/users body.
type createUserRequest struct {
	TelegramID *int64  `json:"telegram_id"`
	Email      *string `json:"email"`
}

// createSubscriptionRequest is the POST /v1/subscriptions body.
type createSubscriptionRequest struct {
	UserID string                     `json:"user_id"`
	Plan   contracts.SubscriptionPlan `json:"plan"`
}

// nodesListResponse is the GET /v1/nodes page.
type nodesListResponse struct {
	Items      []contracts.Node `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

// rotateNodeRequest is the POST /v1/nodes/{id}/rotate body (additive endpoint).
type rotateNodeRequest struct {
	NewEntryIP string `json:"new_entry_ip"`
	Reason     string `json:"reason"`
}

// aggregateDTO is one FieldSignal aggregate from telemetry (additive endpoint).
type aggregateDTO struct {
	NodeID      string         `json:"node_id"`
	Region      string         `json:"region"`
	WindowStart time.Time      `json:"window_start"`
	WindowEnd   time.Time      `json:"window_end"`
	Total       int            `json:"total"`
	BySignal    map[string]int `json:"by_signal"`
	LossPctAvg  float64        `json:"loss_pct_avg"`
	RTTmsAvg    int            `json:"rtt_ms_avg"`
}

// signalAggregateRequest is the POST /v1/signals/aggregate body.
type signalAggregateRequest struct {
	Aggregates []aggregateDTO `json:"aggregates"`
}

// signalAggregateResponse reports what the ingest did.
type signalAggregateResponse struct {
	Applied int                       `json:"applied"`
	Changes []domain.NodeStatusChange `json:"changes"`
}

// subscriptionSetResponse is the GET /v1/users/{id}/subscription-set body — the
// structured Node×Transport set handed to the subscription service (Agent C).
type subscriptionSetResponse struct {
	Revision    int64            `json:"revision"`
	GeneratedAt time.Time        `json:"generated_at"`
	User        contracts.User   `json:"user"`
	Nodes       []contracts.Node `json:"nodes"`
}

// toAggregates converts request DTOs to domain aggregates, validating signal types.
func toAggregates(in []aggregateDTO) ([]domain.SignalAggregate, error) {
	out := make([]domain.SignalAggregate, 0, len(in))
	for _, a := range in {
		bySignal := make(map[contracts.SignalType]int, len(a.BySignal))
		for k, v := range a.BySignal {
			st := contracts.SignalType(k)
			if !st.Valid() {
				return nil, errInvalidSignalType(k)
			}
			bySignal[st] = v
		}
		out = append(out, domain.SignalAggregate{
			NodeID: a.NodeID, Region: a.Region,
			WindowStart: a.WindowStart, WindowEnd: a.WindowEnd,
			Total: a.Total, BySignal: bySignal,
			LossPctAvg: a.LossPctAvg, RTTmsAvg: a.RTTmsAvg,
		})
	}
	return out, nil
}
