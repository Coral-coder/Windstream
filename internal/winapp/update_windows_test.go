//go:build windows

package winapp

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestUpdateTaskXML(t *testing.T) {
	x := updateTaskXML("S-1-5-21-1-2-3-1001", `C:\ProgramData\Windstream\update\1.0.16-Windstream & co.exe`)
	var task struct {
		Actions struct {
			Exec struct {
				Command   string
				Arguments string
			}
		}
	}
	if err := xml.Unmarshal([]byte(x), &task); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(task.Actions.Exec.Command, "& co.exe") || task.Actions.Exec.Arguments != "--update --user S-1-5-21-1-2-3-1001" {
		t.Errorf("exec %+v", task.Actions.Exec)
	}
}
