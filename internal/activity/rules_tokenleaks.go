package activity

func init() {
	rules["token_leak_detected"] = rule{icon: "alert", severity: SevError, text: "{actor} found a leaked {a.identity} token in a {a.channel} and revoked it"}
	rules["reissue_leaked_token"] = rule{icon: "lock", text: "{actor} re-issued a leaked token"}
}
