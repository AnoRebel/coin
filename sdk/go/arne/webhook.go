package arne

import (
	"net/http"
	"time"

	"github.com/anorebel/anorebelcoin/internal/webhook"
)

// WebhookEvent is the signed body delivered to webhook endpoints.
type WebhookEvent = webhook.Event

// VerifyWebhook checks the signature and timestamp window of a delivery.
func VerifyWebhook(publicKey, timestamp string, body []byte, signature string, now time.Time, maxSkew time.Duration) error {
	return webhook.Verify(publicKey, timestamp, body, signature, now, maxSkew)
}

// VerifyWebhookRequest checks the standard headers and returns the delivery ID.
func VerifyWebhookRequest(h http.Header, body []byte, publicKey string, now time.Time, maxSkew time.Duration) (string, error) {
	return webhook.VerifyRequest(h, body, publicKey, now, maxSkew)
}
