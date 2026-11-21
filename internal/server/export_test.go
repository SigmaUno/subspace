package server

import "github.com/SigmaUno/subspace/internal/mergequeue"

func RoutePatterns(s *Server) []string { return s.api.Patterns() }

func SetMergeQueueRunner(r mergequeue.Runner) func() {
	mqRunner = r
	return func() { mqRunner = nil }
}
