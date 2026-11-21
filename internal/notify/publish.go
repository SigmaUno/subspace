package notify

import "context"

func (s *Service) Run(ctx context.Context) { s.startLanes(ctx, nil) }

func (s *Service) Publish(n Notification) {
	for _, l := range s.snapshot() {
		l.enqueue(n)
	}
}
