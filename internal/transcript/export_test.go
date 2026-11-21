package transcript

const MaxCachedTasks = maxCachedTasks

func (s *Service) Cached() int {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	return max(len(s.heads), len(s.briefs))
}
