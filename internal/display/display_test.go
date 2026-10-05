package display

import "testing"

var outs = []Output{
	{Index: 0, Adapter: 0, Output: 0, DeviceName: `\\.\DISPLAY1`, MonitorName: "NVIDIA GeForce RTX 4070", X: 0, Y: 0, Primary: true},
	{Index: 1, Adapter: 0, Output: 1, DeviceName: `\\.\DISPLAY5`, MonitorName: "Virtual Display Driver", X: 2560, Y: 0},
}

func TestSelectOutput(t *testing.T) {
	if o, err := SelectOutput(outs, "1"); err != nil || o.DeviceName != `\\.\DISPLAY5` {
		t.Fatalf("by index: %+v %v", o, err)
	}
	if o, err := SelectOutput(outs, `\\.\display1`); err != nil || o.Index != 0 {
		t.Fatalf("by name: %+v %v", o, err)
	}
	if _, err := SelectOutput(outs, "7"); err == nil {
		t.Fatal("missing index accepted")
	}
}

func TestFindByMonitorName(t *testing.T) {
	o, ok := FindByMonitorName(outs, "virtual display")
	if !ok || o.Index != 1 {
		t.Fatalf("got %+v %v", o, ok)
	}
}

func TestTranslate(t *testing.T) {
	pos := Translate(outs, `\\.\DISPLAY5`)
	if pos[`\\.\DISPLAY5`] != [2]int{0, 0} || pos[`\\.\DISPLAY1`] != [2]int{-2560, 0} {
		t.Fatalf("positions = %v", pos)
	}
}
