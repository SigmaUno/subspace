package store

import "time"

type SpawnLifecycle struct {
	SpawnID          string
	ConnectedAt      time.Time
	IdleSince        time.Time
	StoppedAt        time.Time
	StopReason       string
	Process          string
	ProcessCheckedAt time.Time
}
