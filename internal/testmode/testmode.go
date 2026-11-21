package testmode

import "os"

func Full() bool { return os.Getenv("SUBSPACE_FULL") == "1" }

func Size[T any](reduced, full T) T {
	if Full() {
		return full
	}
	return reduced
}
