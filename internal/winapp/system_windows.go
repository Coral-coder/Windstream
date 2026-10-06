//go:build windows

package winapp

import (
	"fmt"

	"github.com/go-ole/go-ole"
	"github.com/go-ole/go-ole/oleutil"
)

// Everything here talks to Windows through its documented COM APIs (the
// same ones installers and the Settings app use) instead of spawning
// schtasks/netsh in hidden windows.

const (
	taskCreateOrUpdate        = 6
	taskLogonInteractiveToken = 3
)

func withTaskFolder(fn func(folder *ole.IDispatch) error) error {
	return withCOM(func() error {
		svc, err := newDispatch("Schedule.Service")
		if err != nil {
			return fmt.Errorf("task scheduler: %w", err)
		}
		defer svc.Release()
		if _, err := call(svc, "Connect"); err != nil {
			return fmt.Errorf("task scheduler connect: %w", err)
		}
		folder, err := call(svc, "GetFolder", `\`)
		if err != nil {
			return fmt.Errorf("task scheduler folder: %w", err)
		}
		defer folder.Release()
		return fn(folder)
	})
}

func registerTaskXML(xml string) error {
	return withTaskFolder(func(folder *ole.IDispatch) error {
		empty := ole.NewVariant(ole.VT_EMPTY, 0)
		task, err := call(folder, "RegisterTask", taskName, xml, int32(taskCreateOrUpdate),
			&empty, &empty, int32(taskLogonInteractiveToken), &empty)
		if task != nil {
			task.Release()
		}
		return err
	})
}

func withTask(fn func(task *ole.IDispatch) error) error {
	return withTaskFolder(func(folder *ole.IDispatch) error {
		task, err := call(folder, "GetTask", taskName)
		if err != nil {
			return err
		}
		defer task.Release()
		return fn(task)
	})
}

func runTask() error {
	return withTask(func(task *ole.IDispatch) error {
		empty := ole.NewVariant(ole.VT_EMPTY, 0)
		running, err := call(task, "Run", &empty)
		if running != nil {
			running.Release()
		}
		return err
	})
}

func stopTask() {
	_ = withTask(func(task *ole.IDispatch) error {
		_, err := call(task, "Stop", int32(0))
		return err
	})
}

func deleteTask() {
	_ = withTaskFolder(func(folder *ole.IDispatch) error {
		_, err := call(folder, "DeleteTask", taskName, int32(0))
		return err
	})
}

const (
	fwProtocolAny = 256
	fwDirectionIn = 1
	fwActionAllow = 1
	fwProfilesAll = 0x7FFFFFFF
)

func withFirewallRules(fn func(rules *ole.IDispatch) error) error {
	return withCOM(func() error {
		pol, err := newDispatch("HNetCfg.FwPolicy2")
		if err != nil {
			return fmt.Errorf("firewall: %w", err)
		}
		defer pol.Release()
		v, err := oleutil.GetProperty(pol, "Rules")
		if err != nil {
			return fmt.Errorf("firewall rules: %w", err)
		}
		rules := v.ToIDispatch()
		defer rules.Release()
		return fn(rules)
	})
}

// addFirewallRule allows inbound traffic to Windstream.exe only (HTTPS and
// the WebRTC media port, whichever ports it ends up using).
func addFirewallRule() error {
	return withFirewallRules(func(rules *ole.IDispatch) error {
		for i := 0; i < 3; i++ {
			_, _ = call(rules, "Remove", firewallRule)
		}
		rule, err := newDispatch("HNetCfg.FWRule")
		if err != nil {
			return err
		}
		defer rule.Release()
		for _, p := range []struct {
			name string
			val  interface{}
		}{
			{"Name", firewallRule}, {"Description", "Windstream game streaming"},
			{"Grouping", "Windstream"}, {"ApplicationName", InstalledExe},
			{"Protocol", int32(fwProtocolAny)}, {"Direction", int32(fwDirectionIn)},
			{"Action", int32(fwActionAllow)}, {"Profiles", int32(fwProfilesAll)}, {"Enabled", true},
		} {
			if _, err := oleutil.PutProperty(rule, p.name, p.val); err != nil {
				return fmt.Errorf("firewall rule %s: %w", p.name, err)
			}
		}
		_, err = call(rules, "Add", rule)
		return err
	})
}

func removeFirewallRule() {
	_ = withFirewallRules(func(rules *ole.IDispatch) error {
		for i := 0; i < 3; i++ {
			_, _ = call(rules, "Remove", firewallRule)
		}
		return nil
	})
}
