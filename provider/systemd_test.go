package homelab

import (
	"testing"
)

// Ported from src/resources/systemd.test.ts: doing only what is different.

func TestActsNeeded(t *testing.T) {
	running := SystemdUnitState{Unit: "[Unit]\nDescription=x\n", Mode: "0644", Enabled: true, Started: true}
	with := func(f func(*SystemdUnitState)) *SystemdUnitState { s := running; f(&s); return &s }
	cases := []struct {
		name    string
		current *SystemdUnitState
		wanted  SystemdUnitState
		want    unitActs
	}{
		{"nothing when it matches", &running, running, unitActs{}},
		{"restarts a changed unit", &running, *with(func(s *SystemdUnitState) { s.Unit = "[Unit]\nDescription=y\n" }), unitActs{true, false, true}},
		{"enables without restarting", with(func(s *SystemdUnitState) { s.Enabled = false }), running, unitActs{false, true, false}},
		{"starts without rewriting", with(func(s *SystemdUnitState) { s.Started = false }), running, unitActs{false, false, true}},
		{"everything for a new unit", nil, running, unitActs{true, true, true}},
	}
	for _, c := range cases {
		if got := actsNeeded(c.current, c.wanted); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
	if !actsNeeded(with(func(s *SystemdUnitState) { s.Mode = "0600" }), running).rewrite {
		t.Error("rewrites when only the mode differs")
	}
	if got := unitApplyCommand(running, &running); got != "" {
		t.Errorf("an update that changes nothing sends nothing: %q", got)
	}
}

func TestParseUnitRead(t *testing.T) {
	got := parseUnitRead("ActiveState=active UnitFileState=enabled 644\n[Unit]\nDescription=x\n", systemdUnits)
	if !got.Enabled || !got.Started || got.Mode != "0644" || got.UnitFileState != "enabled" || got.Unit != "[Unit]\nDescription=x\n" {
		t.Errorf("%+v", got)
	}
}

func TestStaticRefusal(t *testing.T) {
	if staticRefusal("x", true, "static") == nil || staticRefusal("x", false, "static") != nil || staticRefusal("x", true, "enabled") != nil {
		t.Error("static refusal")
	}
}
