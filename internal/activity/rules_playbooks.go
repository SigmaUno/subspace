package activity

func init() {
	rules["create_playbook"] = rule{icon: "plan", text: "{actor} created playbook {a.name}{note?: }"}
	rules["delete_playbook"] = rule{icon: "plan", severity: SevWarning, text: "{actor} deleted playbook {b.name}"}
	rules["update_playbook"] = rule{icon: "plan", text: "{actor} updated playbook {a.name}"}
}
