package server

func RoutePatterns(s *Server) []string { return s.api.Patterns() }
