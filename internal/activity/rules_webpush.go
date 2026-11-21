package activity

func init() {
	rules["create_push_subscription"] = rule{icon: "send", text: "{actor} subscribed a device to push notifications as {a.id}"}
	rules["delete_push_subscription"] = rule{icon: "x", severity: SevWarning, text: "{actor} removed push subscription {b.id}"}
	rules["expire_push_subscription"] = rule{icon: "x", severity: SevWarning, text: "{actor} removed dead push subscription {b.id}{note?: }"}
}
