package server

import "time"

func SetTokenClock(now func() time.Time) (restore func()) {
	old := tokenClock
	tokenClock = now
	return func() { tokenClock = old }
}

func RoutePatterns(s *Server) []string { return s.api.Patterns() }
