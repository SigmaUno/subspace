//go:build !unix

package loadtest

import "time"

func processCPU() time.Duration { return 0 }
