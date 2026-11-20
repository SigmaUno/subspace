package client

import (
	"errors"
	"io"
	"strings"
	"testing"
)

func TestEventReaderFraming(t *testing.T) {
	in := "id: 1\nevent: hello\ndata: {\"seq\":1}\n\n: ping\n\nid: 2\r\nevent: task.updated\r\ndata: a\r\ndata: b\r\nretry: 5\r\n\r\nevent: cut\ndata: x"
	r := newEventReader(strings.NewReader(in))
	var got []Event
	for {
		ev, err := r.next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, ev)
	}
	if len(got) != 2 || got[0].Name != "hello" || got[1].ID != "2" || got[1].Data != "a\nb" {
		t.Fatalf("frames %+v", got)
	}
}
