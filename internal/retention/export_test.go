package retention

func (s *Service) SetFault(f func(stage string) error) { s.fault = f }
