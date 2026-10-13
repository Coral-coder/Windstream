//go:build windows

package winapp

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/coral-coder/windstream/internal/control"
	"github.com/coral-coder/windstream/internal/update"
)

// updateTaskName is the scheduled task that installs a downloaded update.
// The installer has to outlive the app it replaces, so it is started by
// Task Scheduler rather than as a child of the app (whose job object would
// end it together with the app).
const updateTaskName = "Windstream Update"

// UpdateFailedFile records why the last automatic update did not install.
func UpdateFailedFile() string { return filepath.Join(DataDir, "update", control.UpdateFailedNote) }

// updateTaskXML runs exe --update as the signed-in user, elevated like
// Windstream itself (no prompt), on demand only.
func updateTaskXML(userSID, exe string) string {
	return `<Task version="1.2" xmlns="http://schemas.microsoft.com/windows/2004/02/mit/task">
  <RegistrationInfo><Author>Windstream</Author><Description>Installs Windstream updates. Remove it via Apps &amp; features &gt; Windstream.</Description></RegistrationInfo>
  <Triggers/>
  <Principals><Principal id="Author"><UserId>` + userSID + `</UserId><LogonType>InteractiveToken</LogonType><RunLevel>HighestAvailable</RunLevel></Principal></Principals>
  <Settings>
    <MultipleInstancesPolicy>IgnoreNew</MultipleInstancesPolicy>
    <DisallowStartIfOnBatteries>false</DisallowStartIfOnBatteries>
    <StopIfGoingOnBatteries>false</StopIfGoingOnBatteries>
    <AllowHardTerminate>true</AllowHardTerminate>
    <AllowStartOnDemand>true</AllowStartOnDemand>
    <Enabled>true</Enabled>
    <Hidden>true</Hidden>
    <ExecutionTimeLimit>PT10M</ExecutionTimeLimit>
    <Priority>4</Priority>
  </Settings>
  <Actions Context="Author">
    <Exec>
      <Command>` + xmlEscape(exe) + `</Command>
      <Arguments>--update --user ` + userSID + `</Arguments>
      <WorkingDirectory>` + xmlEscape(filepath.Dir(exe)) + `</WorkingDirectory>
    </Exec>
  </Actions>
</Task>
`
}

// UpdateAsset names the release download for this PC.
func (p *platform) UpdateAsset() string { return update.AssetName(runtime.GOOS, runtime.GOARCH) }

// UnattendedUpdates: Windows keeps every permission across updates.
func (p *platform) UnattendedUpdates() bool { return true }

// InstallUpdate hands the downloaded exe to Task Scheduler, which runs it
// with --update: it stops this app, installs itself and starts again.
func (p *platform) InstallUpdate(file, version string) error {
	dir := filepath.Join(DataDir, "update")
	if !strings.EqualFold(filepath.Dir(filepath.Clean(file)), dir) {
		return errors.New("update file is not in the update folder")
	}
	sid, err := currentUserSID()
	if err != nil {
		return err
	}
	_ = os.Remove(UpdateFailedFile())
	if err := registerNamedTask(updateTaskName, updateTaskXML(sid, file)); err != nil {
		return fmt.Errorf("prepare update: %w", err)
	}
	if err := runNamedTask(updateTaskName); err != nil {
		return fmt.Errorf("start update: %w", err)
	}
	return nil
}

// UpdateFromArgs implements --update (started by the update task): install
// this exe over the running Windstream. It must never leave the PC without
// Windstream, so if anything fails the installed version is started again
// and the reason recorded for the dashboard instead of shown in a dialog
// nobody may be there to see.
func UpdateFromArgs(args []string) error {
	sid := ""
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--user" {
			sid = args[i+1]
		}
	}
	err := Install(Version, sid)
	if err != nil {
		_ = os.MkdirAll(filepath.Dir(UpdateFailedFile()), 0o755)
		msg := fmt.Sprintf("%s: Windstream %s could not be installed: %v\n", time.Now().Format(time.RFC3339), Version, err)
		_ = os.WriteFile(UpdateFailedFile(), []byte(msg), 0o644)
		_ = runTask() // bring the installed version back
	}
	return nil
}
