package webpush_test

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/servertest"
	"github.com/SigmaUno/subspace/internal/webpush"
)

var b64 = base64.RawURLEncoding

func keys(t *testing.T) map[string]any {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	auth := make([]byte, 16)
	_, _ = rand.Read(auth)
	return map[string]any{"p256dh": b64.EncodeToString(k.PublicKey().Bytes()), "auth": b64.EncodeToString(auth)}
}

func sub(t *testing.T, endpoint string) map[string]any {
	t.Helper()
	return map[string]any{"endpoint": endpoint, "expirationTime": nil, "keys": keys(t)}
}

func TestSubscriptionLifecycle(t *testing.T) {
	e := servertest.New(t)

	code, key := e.REST("GET", "/api/v1/push/vapid-key", nil)
	raw, err := b64.DecodeString(fmt.Sprint(key["public_key"]))
	if code != 200 || err != nil || len(raw) != 65 || raw[0] != 4 || len(key) != 1 {
		t.Fatalf("vapid key: %d %v", code, key)
	}
	if _, again := e.REST("GET", "/api/v1/push/vapid-key", nil); again["public_key"] != key["public_key"] {
		t.Fatal("vapid key changed between requests")
	}

	endpoint := "https://fcm.googleapis.com/fcm/send/abc:def"
	body := sub(t, endpoint)
	body["name"] = "  Pixel   phone "
	code, created := e.REST("POST", "/api/v1/push/subscriptions", body)
	if code != 201 || created["name"] != "Pixel phone" || created["host"] != "fcm.googleapis.com" || created["failures"] != 0.0 {
		t.Fatalf("create: %d %v", code, created)
	}
	want := []string{"created_at", "expires_at", "failures", "host", "id", "last_success_at", "name"}
	var got []string
	for k := range created {
		got = append(got, k)
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("subscription fields %v, want %v", got, want)
	}
	code, again := e.REST("POST", "/api/v1/push/subscriptions", sub(t, endpoint))
	if code != 200 || again["id"] != created["id"] {
		t.Fatalf("resubscribe: %d %v", code, again)
	}
	code, list := e.REST("GET", "/api/v1/push/subscriptions", nil)
	if subs, _ := list["subscriptions"].([]any); code != 200 || len(subs) != 1 {
		t.Fatalf("list: %d %v", code, list)
	}

	code, tok := e.REST("POST", "/api/v1/tokens", map[string]any{"name": "phone viewer", "scope": "viewer"})
	if code != 201 {
		t.Fatalf("viewer token: %d %v", code, tok)
	}
	secret := tok["secret"].(string)
	if code, _ := e.RESTAs(secret, "GET", "/api/v1/push/subscriptions", nil); code != 200 {
		t.Fatalf("viewer list: %d", code)
	}
	for _, m := range []string{"POST", "DELETE"} {
		if code, out := e.RESTAs(secret, m, "/api/v1/push/subscriptions", sub(t, "https://fcm.googleapis.com/fcm/send/x")); code != 403 {
			t.Fatalf("viewer %s: %d %v", m, code, out)
		}
	}

	for _, bad := range []map[string]any{
		{},
		{"id": created["id"], "endpoint": endpoint},
		{"endpoint": endpoint, "extra": true},
	} {
		if code, out := e.REST("DELETE", "/api/v1/push/subscriptions", bad); code != 400 {
			t.Fatalf("delete %v: %d %v", bad, code, out)
		}
	}
	if code, out := e.REST("DELETE", "/api/v1/push/subscriptions", map[string]any{"endpoint": "https://FCM.googleapis.com/fcm/send/abc:def"}); code != 204 {
		t.Fatalf("delete by endpoint: %d %v", code, out)
	}
	if code, _ := e.REST("DELETE", "/api/v1/push/subscriptions", map[string]any{"id": created["id"]}); code != 404 {
		t.Fatalf("delete twice: %d", code)
	}

	for i := range webpush.MaxSubs {
		if code, out := e.REST("POST", "/api/v1/push/subscriptions", sub(t, fmt.Sprintf("https://updates.push.services.mozilla.com/wpush/v2/%d", i))); code != 201 {
			t.Fatalf("subscription %d: %d %v", i, code, out)
		}
	}
	if code, out := e.REST("POST", "/api/v1/push/subscriptions", sub(t, "https://web.push.apple.com/one-too-many")); code != 409 {
		t.Fatalf("over the limit: %d %v", code, out)
	}
	_, list = e.REST("GET", "/api/v1/push/subscriptions", nil)
	last := list["subscriptions"].([]any)[webpush.MaxSubs-1].(map[string]any)
	if code, _ := e.REST("DELETE", "/api/v1/push/subscriptions", map[string]any{"id": last["id"]}); code != 204 {
		t.Fatalf("delete by id: %d", code)
	}

	_, audit := e.REST("GET", "/api/v1/audit?tool=create_push_subscription", nil)
	if evs := audit["events"].([]any); len(evs) != 2+webpush.MaxSubs || !strings.HasPrefix(fmt.Sprint(evs[0].(map[string]any)["note"]), "token=root") {
		t.Fatalf("create audit: %v", audit)
	}
	_, audit = e.REST("GET", "/api/v1/audit?tool=delete_push_subscription", nil)
	if evs := audit["events"].([]any); len(evs) != 2 {
		t.Fatalf("delete audit: %v", audit)
	}
	_, feed := e.REST("GET", "/api/v1/activity?limit=5", nil)
	if items := feed["items"].([]any); !strings.Contains(fmt.Sprint(items[0].(map[string]any)["text"]), "push subscription") {
		t.Fatalf("activity: %v", items[0])
	}
}

func TestSubscriptionEndpointValidation(t *testing.T) {
	e := servertest.New(t)
	accepted := []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://FCM.googleapis.com:443/wp/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/QGz",
		"https://wns2-par02p.notify.windows.com/w/?token=abc",
	}
	for _, u := range accepted {
		if code, out := e.REST("POST", "/api/v1/push/subscriptions", sub(t, u)); code != 201 {
			t.Errorf("%s rejected: %d %v", u, code, out)
		}
	}
	rejected := []string{
		"",
		"http://fcm.googleapis.com/fcm/send/abc",
		"ftp://fcm.googleapis.com/abc",
		"//fcm.googleapis.com/abc",
		"https://fcm.googleapis.com.evil.example/abc",
		"https://evilfcm.googleapis.com/abc",
		"https://push.apple.com/abc",
		"https://evilpush.apple.com/abc",
		"https://example.com/push",
		"https://user:pass@fcm.googleapis.com/abc",
		"https://fcm.googleapis.com:8443/abc",
		"https://fcm.googleapis.com./abc",
		"https://fcm.googleapis.com/abc#frag",
		"https://fcm.googleapis.com/a b",
		"https://fcm.googleapis.com\\@evil.example/abc",
		"https://localhost/push",
		"https://127.0.0.1/push",
		"https://10.0.0.8/push",
		"https://169.254.169.254/latest/meta-data",
		"https://[::1]/push",
		"https://[fd00:ec2::254]/push",
		"https://0x7f000001/push",
		"https://2130706433/push",
		"https://fcm.googleapis.com/" + strings.Repeat("a", 2048),
	}
	for _, u := range rejected {
		if code, out := e.REST("POST", "/api/v1/push/subscriptions", sub(t, u)); code != 400 {
			t.Errorf("%q accepted: %d %v", u, code, out)
		}
	}
	good := keys(t)
	pt := make([]byte, 65)
	pt[0] = 4
	for name, k := range map[string]map[string]any{
		"short auth":     {"p256dh": good["p256dh"], "auth": "AAAA"},
		"not on curve":   {"p256dh": b64.EncodeToString(pt), "auth": good["auth"]},
		"compressed key": {"p256dh": b64.EncodeToString(make([]byte, 33)), "auth": good["auth"]},
		"missing":        {},
	} {
		body := map[string]any{"endpoint": accepted[0], "keys": k}
		if code, out := e.REST("POST", "/api/v1/push/subscriptions", body); code != 400 {
			t.Errorf("%s accepted: %d %v", name, code, out)
		}
	}
	for name, body := range map[string]map[string]any{
		"unknown field": {"endpoint": accepted[0], "keys": good, "subscription": "x"},
		"expired":       {"endpoint": accepted[0], "keys": good, "expirationTime": 1000},
		"long name":     {"endpoint": accepted[0], "keys": good, "name": strings.Repeat("n", 101)},
	} {
		if code, out := e.REST("POST", "/api/v1/push/subscriptions", body); code != 400 {
			t.Errorf("%s accepted: %d %v", name, code, out)
		}
	}
	_, list := e.REST("GET", "/api/v1/push/subscriptions", nil)
	if n := len(list["subscriptions"].([]any)); n != len(accepted) {
		t.Fatalf("%d subscriptions stored, want %d", n, len(accepted))
	}
}
