package openapi

import (
	"net/http"
	"reflect"

	"github.com/SigmaUno/subspace/internal/webpush"
)

func init() {
	componentNames[reflect.TypeFor[webpush.Input]()] = "PushSubscriptionInput"
	componentNames[reflect.TypeFor[webpush.InputKeys]()] = "PushSubscriptionKeys"
	componentNames[reflect.TypeFor[webpush.Key]()] = "PushVAPIDKey"
	componentNames[reflect.TypeFor[webpush.Remove]()] = "PushSubscriptionRemove"
	componentNames[reflect.TypeFor[webpush.Subscription]()] = "PushSubscription"

	Register(
		get("/api/v1/push/vapid-key", "VAPID public key for browser push subscriptions", webpush.Key{}),
		get("/api/v1/push/subscriptions", "List Web Push subscriptions", list[webpush.Subscription]("subscriptions")),
		post("/api/v1/push/subscriptions", "Subscribe a browser to Web Push notifications; 200 when the endpoint was already subscribed", webpush.Input{}, webpush.Subscription{}).S(created),
		send(http.MethodDelete, "/api/v1/push/subscriptions", "Remove a Web Push subscription by id or endpoint", webpush.Remove{}, nil).S(gone),
	)
}
