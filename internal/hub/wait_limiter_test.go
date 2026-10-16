package hub

import "testing"

func TestWaitLimiterCapsPerHost(t *testing.T) {
	l := newWaitLimiter(2)
	if !l.acquire("a") || !l.acquire("a") || l.acquire("a") {
		t.Fatal("cap of 2 not enforced")
	}
	if !l.acquire("b") {
		t.Fatal("other host must have its own budget")
	}
	l.release("a")
	if !l.acquire("a") {
		t.Fatal("released slot not reusable")
	}
}

func TestParseWait(t *testing.T) {
	ok := map[string]int{"": 0, "0": 0, "5": 5, "50": 50, "50s": 50, "1500ms": 1}
	for in, secs := range ok {
		d, good := parseWait(in)
		if !good || int(d.Seconds()) != secs {
			t.Errorf("%q -> %v %v", in, d, good)
		}
	}
	for _, in := range []string{"51", "-1", "x", "51s"} {
		if _, good := parseWait(in); good {
			t.Errorf("%q accepted", in)
		}
	}
}
