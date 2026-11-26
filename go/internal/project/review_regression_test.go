package project

import (
	"os"
	"testing"
)

func TestReviewFrameCountAtStoreBoundaries(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	p, err := s.Create(CreateInput{Name: "review", Paths: []string{root}, FrameCount: 1})
	if err != nil {
		t.Fatal(err)
	}
	if p.FrameCount != 3 {
		t.Errorf("create count=%d", p.FrameCount)
	}
	two := 2
	p, err = s.Update(p.ID, UpdateInput{FrameCount: &two})
	if err != nil {
		t.Fatal(err)
	}
	if p.FrameCount != 3 {
		t.Errorf("update count=%d", p.FrameCount)
	}
	st := DefaultSettings()
	st.DefaultFrameCount = 1
	if err := s.SaveSettings(st); err != nil {
		t.Fatal(err)
	}
	st, err = s.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.DefaultFrameCount != 3 {
		t.Errorf("settings count=%d", st.DefaultFrameCount)
	}
	if err := os.WriteFile(s.projectsPath(), []byte(`[{"id":"legacy","frameCount":1}]`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.settingsPath(), []byte(`{"defaultFrameCount":2}`), 0o600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	legacy, _ := reloaded.Get("legacy")
	if legacy.FrameCount != 3 {
		t.Errorf("loaded project count=%d", legacy.FrameCount)
	}
	st, err = reloaded.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if st.DefaultFrameCount != 3 {
		t.Errorf("loaded settings count=%d", st.DefaultFrameCount)
	}
}
