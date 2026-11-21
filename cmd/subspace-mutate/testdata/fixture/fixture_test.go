package fixture

import "testing"

func TestFixture(t *testing.T) {
	if !Positive(5) || Positive(-5) {
		t.Fatal("Positive")
	}
	if q, err := Div(6, 3); err != nil || q != 2 {
		t.Fatal("Div")
	}
	if Sub(5, 3) != 2 {
		t.Fatal("Sub")
	}
	if Countdown(3) != 3 {
		t.Fatal("Countdown")
	}
	if !Both(true, true) || Both(true, false) {
		t.Fatal("Both")
	}
	if h, err := Half(4); err != nil || h != 2 {
		t.Fatal("Half")
	}
	if Name(1) != "one" {
		t.Fatal("Name")
	}
}
