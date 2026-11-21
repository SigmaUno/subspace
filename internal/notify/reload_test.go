package notify

import (
	"slices"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestReloadReplacesSinksAndKeepsOldOnInvalidConfig(t *testing.T) {
	a, b := newCollector(t), newCollector(t)
	h := start(t, Config{Sinks: []Sink{webhook(a)}})

	h.publish("questions", "question.created", question("Q-1", "T-1"))
	h.flush()
	eventually(t, "delivery to first sink", func() bool { return a.count() == 1 })

	_, err := h.svc.PrepareReload(envOf(map[string]string{EnvWebhookURL: "ftp://example.test/hook"}))
	if err == nil {
		t.Fatal("invalid sink url accepted")
	}
	h.publish("questions", "question.created", question("Q-2", "T-1"))
	h.flush()
	eventually(t, "old sink still used", func() bool { return a.count() == 2 })

	plan, err := h.svc.PrepareReload(envOf(map[string]string{EnvWebhookURL: b.srv.URL, EnvAllowPrivate: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	plan.Commit()
	h.publish("questions", "question.created", question("Q-3", "T-1"))
	h.flush()
	eventually(t, "new sink used", func() bool { return b.count() == 1 })
	if a.count() != 2 {
		t.Fatalf("retired sink received %d notifications", a.count())
	}
	if got := h.svc.SinkNames(); !slices.Equal(got, []string{"webhook"}) {
		t.Fatalf("sinks %v", got)
	}
}

func TestReloadEnablesServiceStartedWithoutSinks(t *testing.T) {
	c := newCollector(t)
	h := start(t, Config{})
	if h.svc.Enabled() {
		t.Fatal("enabled without sinks")
	}
	plan, err := h.svc.PrepareReload(envOf(map[string]string{EnvWebhookURL: c.srv.URL, EnvAllowPrivate: "true"}))
	if err != nil {
		t.Fatal(err)
	}
	plan.Commit()
	h.publish("questions", "question.created", question("Q-1", "T-1"))
	h.flush()
	eventually(t, "delivery", func() bool { return c.count() == 1 })

	plan, err = h.svc.PrepareReload(envOf(nil))
	if err != nil {
		t.Fatal(err)
	}
	plan.Commit()
	if h.svc.Enabled() {
		t.Fatal("still enabled after removing every sink")
	}
}
