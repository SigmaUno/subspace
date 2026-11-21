package openapi

func init() {
	Register(
		get("/readyz", "Readiness: 200 while this instance takes traffic, 503 with an empty body while it migrates, drains or runs read-only", nil).Open(),
	)
}
