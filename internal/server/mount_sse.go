package server

import (
	"os"
	"strconv"
)

const envSSEReplay = "SUBSPACE_SSE_REPLAY"

func init() {
	registerMount(func(d Deps) {
		v := os.Getenv(envSSEReplay)
		if v == "" {
			return
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			d.Log.Warn("invalid SSE replay size, using default", "env", envSSEReplay)
			return
		}
		if h, ok := d.Hub.(interface{ SetReplay(int) }); ok {
			h.SetReplay(n)
		}
	})
}
