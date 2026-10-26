package inspect

import "testing"

func TestNoteReachFiresImmediatelyRecoversAfterTwoOK(t *testing.T) {
	w := &Worker{}
	fire, recover := w.noteReach("c1", false)
	if !fire || recover {
		t.Fatalf("first fail should fire: fire=%v recover=%v", fire, recover)
	}
	fire, recover = w.noteReach("c1", true)
	if fire || recover {
		t.Fatalf("first ok should not recover: fire=%v recover=%v", fire, recover)
	}
	fire, recover = w.noteReach("c1", true)
	if fire || !recover {
		t.Fatalf("second ok should recover: fire=%v recover=%v", fire, recover)
	}
}

func TestNoteReachFailResetsOKStreak(t *testing.T) {
	w := &Worker{}
	w.noteReach("c1", false)
	w.noteReach("c1", true)
	fire, recover := w.noteReach("c1", false)
	if !fire || recover {
		t.Fatalf("fail after one ok: fire=%v recover=%v", fire, recover)
	}
	_, recover = w.noteReach("c1", true)
	if recover {
		t.Fatal("ok streak must restart after fail")
	}
	_, recover = w.noteReach("c1", true)
	if !recover {
		t.Fatal("two oks after fail should recover")
	}
}

func TestDropReachClearsStreak(t *testing.T) {
	w := &Worker{}
	w.noteReach("c1", false)
	w.noteReach("c1", true)
	w.dropReach("c1")
	_, recover := w.noteReach("c1", true)
	if recover {
		t.Fatal("streak should restart after drop")
	}
}
