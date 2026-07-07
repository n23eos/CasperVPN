package httpapi

import (
	"io"
	"net/http"
)

// maxWebhookBody caps webhook payload size to blunt memory-exhaustion attempts.
const maxWebhookBody = 1 << 20 // 1 MiB

// webhookSignature extracts the provider signature header. Different gateways use
// different header names; billing accepts the common ones. The actual verification
// (HMAC) happens inside each gateway's ParseWebhook, so a forged header still fails.
func webhookSignature(r *http.Request) string {
	for _, h := range []string{"BTCPay-Sig", "X-Signature", "X-Webhook-Signature"} {
		if v := r.Header.Get(h); v != "" {
			return v
		}
	}
	return ""
}

// readLimitedBody reads at most maxWebhookBody bytes from the request.
func readLimitedBody(r *http.Request) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, maxWebhookBody))
}
