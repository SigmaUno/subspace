package activity

func init() {
	rules["create_policy"] = rule{icon: "lock", text: "{actor} created policy {a.id} ({a.name})"}
	rules["update_policy"] = rule{icon: "lock", text: "{actor} updated policy {a.id} ({a.name}) to version {a.version}"}
	rules["delete_policy"] = rule{icon: "lock", severity: SevWarning, text: "{actor} deleted policy {b.id} ({b.name})"}
	rules["policy_hold"] = rule{icon: "lock", severity: SevWarning, text: "{actor} needs owner approval for {a.action}{task? on }: {a.message}"}
	rules["policy_warn"] = rule{icon: "lock", severity: SevWarning, text: "Policy {a.policy} warned {actor} on {a.action}{task? of }: {a.message}"}
	rules["approve_policy_hold"] = rule{icon: "check", severity: SevSuccess, text: "{actor} approved {a.action} held as {a.hold_id}{note?: }"}
	rules["reject_policy_hold"] = rule{icon: "x", severity: SevWarning, text: "{actor} rejected {a.action} held as {a.hold_id}{note?: }"}
}
