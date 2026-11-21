package agentout

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/SigmaUno/subspace/internal/testmode"
)

var junk = []string{
	"\x1b[2J", "\x1b[H", "\x1b[?1049h", "\x1b[6n", "\x1b[1;31m", "\x1b[0m", "\x1b[5m", "\x1b[38;5;196m",
	"\x1b]0;title\x07", "\x1b]8;;https://evil.example\x1b\\", "\x1b]52;c;cm0gLXJmIC8=\x07", "\x1bPq#0\x1b\\", "\x1b_apc\x1b\\",
	"\x00", "\x07", "\x08", "\x7f", "\u202e", "\u2066", "\u200f", "\u0085", "\u009b",
}

var fragments = []string{
	"\x1b", "\x1b[", "\x1b]", "\x1bP", "\x1b[3", "\x1b[38;2;", "\x1b]0;", "\x07", "\x1b\\", "\u009c", "m", ";", "0", "31",
	"\r", "\r\n", "\n", "\t", "\xff", "\xc3", "\u202e", "é", "日本", "plain text", " ", "[", "]",
}

func secrets(rng *rand.Rand) []string {
	alnum := func(n int) string {
		const set = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
		b := make([]byte, n)
		for i := range b {
			b[i] = set[rng.IntN(len(set))]
		}
		return string(b)
	}
	return []string{
		"ghp_" + alnum(36),
		"github_pat_" + alnum(40),
		"sk-ant-" + alnum(40),
		"sk-proj-" + alnum(40),
		"AKIA" + strings.ToUpper(alnum(16)),
		"eyJ" + alnum(12) + ".eyJ" + alnum(20) + "." + alnum(20),
		"Authorization: Bearer " + alnum(40),
		"password=" + alnum(24),
		"GITHUB_TOKEN=ghp_" + alnum(36),
	}
}

func split(rng *rand.Rand, s string) string {
	var b strings.Builder
	cut := 5 + rng.IntN(5)
	for i, r := range s {
		if i >= cut && rng.IntN(3) == 0 {
			b.WriteString(junk[rng.IntN(len(junk))])
		}
		b.WriteRune(r)
	}
	return b.String()
}

func TestCleanCorpusHolds(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, seed*104729))
			for i := range 6000 {
				var in strings.Builder
				for range 1 + rng.IntN(40) {
					switch rng.IntN(3) {
					case 0:
						in.WriteString(junk[rng.IntN(len(junk))])
					case 1:
						in.WriteString(fragments[rng.IntN(len(fragments))])
					default:
						in.WriteByte(byte(rng.IntN(256)))
					}
				}
				lines, _ := Clean(in.String(), MaxChunk)
				for _, l := range lines {
					checkClean(t, l)
					if strings.ContainsAny(l, "\r\n") {
						t.Fatalf("case %d: line break inside %q (input %q)", i, l, in.String())
					}
				}
			}
		})
	}
}

func TestCleanCorpusMasksPlantedSecrets(t *testing.T) {
	for _, seed := range []uint64{1, 2, 3} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			rng := rand.New(rand.NewPCG(seed, seed*7919))
			for i := range testmode.Size(500, 1500) {
				var in strings.Builder
				var planted []string
				for range 1 + rng.IntN(12) {
					if rng.IntN(2) == 0 {
						in.WriteString(junk[rng.IntN(len(junk))])
						continue
					}
					all := secrets(rng)
					s := all[rng.IntN(len(all))]
					planted = append(planted, s)
					in.WriteString(" ")
					if rng.IntN(2) == 0 {
						in.WriteString(split(rng, s))
					} else {
						in.WriteString(s)
					}
					in.WriteString(" ")
				}
				lines, _ := Clean(in.String(), MaxChunk)
				var out strings.Builder
				for _, l := range lines {
					checkClean(t, l)
					out.WriteString(StripSGR(l))
					out.WriteString("\n")
				}
				for _, s := range planted {
					if tail := s[len(s)-14:]; strings.Contains(out.String(), tail) {
						t.Fatalf("case %d: secret tail %q survived in %q (input %q)", i, tail, out.String(), in.String())
					}
				}
			}
		})
	}
}
