package agentout

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

func checkInvariants(t *testing.T, b *Buffer) {
	t.Helper()
	total := 0
	for id, r := range b.agents {
		bytes := 0
		for i := 0; i < r.len(); i++ {
			l := r.at(i)
			bytes += l.cost()
			if i > 0 && l.Seq != r.at(i-1).Seq+1 {
				t.Fatalf("%s: seq gap %d after %d", id, l.Seq, r.at(i-1).Seq)
			}
		}
		if bytes != r.bytes {
			t.Fatalf("%s: tracked %d bytes, actual %d", id, r.bytes, bytes)
		}
		if r.len() > b.lim.Lines || r.bytes > b.lim.Bytes {
			t.Fatalf("%s: %d lines / %d bytes over the per-agent limit", id, r.len(), r.bytes)
		}
		if (r.len() > 0) != (r.elem != nil) {
			t.Fatalf("%s: %d lines but lru membership %v", id, r.len(), r.elem != nil)
		}
		if r.len() > 0 && r.at(r.len()-1).Seq != r.seq {
			t.Fatalf("%s: newest line %d but seq %d", id, r.at(r.len()-1).Seq, r.seq)
		}
		total += bytes
	}
	if total != b.bytes {
		t.Fatalf("global tracked %d bytes, actual %d", b.bytes, total)
	}
	if b.bytes > b.lim.GlobalBytes {
		t.Fatalf("global %d bytes over cap %d", b.bytes, b.lim.GlobalBytes)
	}
}

func TestBufferGlobalCapThousandAgents(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, seed*7919))
			lim := Limits{Lines: 200, Bytes: 16 << 10, GlobalBytes: 4 << 20}
			b := NewBuffer(lim)
			const agents = 1000
			sent := make([]int64, agents)
			last := make([]time.Time, agents)
			clock := time.Unix(0, 0)
			for step := range 60000 {
				a := rng.IntN(agents)
				if rng.IntN(4) == 0 {
					a = rng.IntN(50)
				}
				n := 1 + rng.IntN(6)
				texts := make([]string, n)
				for i := range texts {
					texts[i] = strings.Repeat("x", rng.IntN(400))
				}
				clock = clock.Add(time.Millisecond)
				out := b.Append(fmt.Sprintf("a%04d", a), nil, "stdout", texts, clock)
				for i, l := range out {
					if l.Seq != sent[a]+int64(i)+1 {
						t.Fatalf("agent %d: seq %d want %d", a, l.Seq, sent[a]+int64(i)+1)
					}
				}
				sent[a] += int64(n)
				last[a] = clock
				if step%997 == 0 {
					checkInvariants(t, b)
				}
			}
			checkInvariants(t, b)

			newest, oldest := 0, 0
			for a := range agents {
				if last[a].After(last[newest]) {
					newest = a
				}
				if sent[a] > 0 && (sent[oldest] == 0 || last[a].Before(last[oldest])) {
					oldest = a
				}
			}
			p := b.Since(fmt.Sprintf("a%04d", newest), 0, MaxLimit, "")
			if len(p.Lines) == 0 || p.Last != sent[newest] || p.Lines[len(p.Lines)-1].Seq != sent[newest] {
				t.Fatalf("most recent agent lost its newest line: %+v", p.Last)
			}
			if bytes, n := b.Usage(); bytes > lim.GlobalBytes || n == agents {
				t.Fatalf("usage %d bytes over %d agents; expected eviction under the global cap", bytes, n)
			}
			if p := b.Since(fmt.Sprintf("a%04d", oldest), 0, MaxLimit, ""); p.Last != sent[oldest] {
				t.Fatalf("evicted agent forgot its seq: %d want %d", p.Last, sent[oldest])
			}
		})
	}
}

func TestBufferPerAgentBounds(t *testing.T) {
	b := NewBuffer(Limits{Lines: 2000, Bytes: 512 << 10, GlobalBytes: 64 << 20})
	ts := time.Unix(0, 0)
	for range 5000 {
		b.Append("w1", nil, "stdout", []string{"short"}, ts)
	}
	checkInvariants(t, b)
	p := b.Since("w1", 0, MaxLimit, "")
	if len(p.Lines) != 2000 || p.First != 3001 || p.Last != 5000 {
		t.Fatalf("line cap: %d lines, first %d last %d", len(p.Lines), p.First, p.Last)
	}
	big := strings.Repeat("y", 4000)
	for range 400 {
		b.Append("w2", nil, "stderr", []string{big}, ts)
	}
	checkInvariants(t, b)
	if r := b.agents["w2"]; r.bytes > 512<<10 || r.len() != (512<<10)/(4000+lineOverhead) {
		t.Fatalf("byte cap: %d bytes in %d lines", r.bytes, r.len())
	}

	task := "T-1"
	b.Append("w3", &task, "stdout", []string{"a", "b"}, ts)
	b.Append("w3", nil, "stdout", []string{"c"}, ts)
	b.Append("w3", &task, "stdout", []string{"d"}, ts)
	if p := b.Since("w3", 1, 10, "T-1"); len(p.Lines) != 2 || p.Lines[0].Text != "b" || p.Lines[1].Text != "d" {
		t.Fatalf("task filter: %+v", p.Lines)
	}
	if p := b.Since("w3", 0, 2, ""); len(p.Lines) != 2 || p.Lines[0].Text != "c" || !p.More {
		t.Fatalf("tail: %+v", p)
	}
	if p := b.Since("w3", 1, 1, ""); len(p.Lines) != 1 || p.Lines[0].Text != "b" || !p.More {
		t.Fatalf("since page: %+v", p)
	}
}
