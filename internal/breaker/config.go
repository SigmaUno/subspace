package breaker

import (
	"fmt"
	"strconv"
	"time"
)

const (
	EnvErrorRate  = "SUBSPACE_BREAKER_ERROR_RATE"
	EnvWindow     = "SUBSPACE_BREAKER_WINDOW"
	EnvMinSamples = "SUBSPACE_BREAKER_MIN_SAMPLES"
	EnvCooldown   = "SUBSPACE_BREAKER_COOLDOWN"
)

func ConfigFromEnv(getenv func(string) string) (Config, error) {
	var c Config
	if v := getenv(EnvErrorRate); v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f >= 1 {
			return Config{}, fmt.Errorf("%s must be a number above 0 and below 1", EnvErrorRate)
		}
		c.Threshold = f
	}
	if v := getenv(EnvMinSamples); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("%s must be a positive integer", EnvMinSamples)
		}
		c.MinSamples = n
	}
	for _, d := range []struct {
		name string
		dst  *time.Duration
	}{{EnvWindow, &c.Window}, {EnvCooldown, &c.Cooldown}} {
		if v := getenv(d.name); v != "" {
			p, err := time.ParseDuration(v)
			if err != nil || p <= 0 {
				return Config{}, fmt.Errorf("%s must be a positive duration", d.name)
			}
			*d.dst = p
		}
	}
	return c.withDefaults(), nil
}
