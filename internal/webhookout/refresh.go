package webhookout

import "context"

func (s *Service) Refresh(ctx context.Context) error { return s.refresh(ctx) }
