package app

import (
	"context"
	"log/slog"

	"tamizchat/internal/bots"
	"tamizchat/internal/media"
)

// webhookRouter verifies LiveKit's callbacks and turns them into actions.
//
// The only event TamizChat acts on today is a finished ingress, which is how a
// music bot learns its track ran out and moves to the next one. Everything else
// is logged and ignored, so a new LiveKit event never breaks the endpoint.
type webhookRouter struct {
	media *media.Manager
	bots  *bots.Manager
}

// HandleWebhook implements httpapi.LiveKitWebhooks.
func (w webhookRouter) HandleWebhook(ctx context.Context, authHeader string, body []byte) error {
	event, err := w.media.VerifyWebhook(authHeader, body)
	if err != nil {
		return err
	}

	switch event.Event {
	case media.EventIngressEnded:
		slog.Debug("livekit ingress ended", "ingress", event.Ingress.IngressID)
		w.bots.TrackEnded(ctx, event.Ingress.IngressID)
	case media.EventIngressStarted:
		slog.Debug("livekit ingress started", "ingress", event.Ingress.IngressID)
	default:
		slog.Debug("ignoring livekit webhook", "event", event.Event)
	}
	return nil
}
