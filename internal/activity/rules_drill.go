package activity

func init() {
	rules["restore_drill"] = rule{
		icon:     "archive",
		text:     "{actor} ran restore drill {a.id}{a.backup? on }: {a.status}",
		byStatus: map[string]string{"failed": "{actor} found restore drill {a.id} failed at {a.failed_check}: {a.error}"},
		warnIf:   "a.failed_check",
	}
}
