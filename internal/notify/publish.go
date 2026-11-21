package notify

import "context"

func (s *Service) Run(ctx context.Context) {
	for _, l := range s.lanes {
		s.wg.Go(func() { l.run(ctx) })
	}
}

func (s *Service) Publish(n Notification) {
	for _, l := range s.lanes {
		l.enqueue(n)
	}
}
