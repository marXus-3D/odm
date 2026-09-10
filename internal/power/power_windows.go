//go:build windows

// Package power performs the "when everything is done" actions: sleep,
// hibernate, shut down or restart the machine.
package power

import (
	"fmt"
	"os/exec"
	"syscall"
)

// Action is what to do once the queue drains.
type Action string

const (
	None      Action = "none"
	ExitDM    Action = "exit"
	Sleep     Action = "sleep"
	Hibernate Action = "hibernate"
	Shutdown  Action = "shutdown"
	Restart   Action = "restart"
)

// Valid reports whether a is a known action.
func Valid(a Action) bool {
	switch a {
	case None, ExitDM, Sleep, Hibernate, Shutdown, Restart:
		return true
	}
	return false
}

// Actions lists everything selectable, in menu order.
func Actions() []Action {
	return []Action{None, ExitDM, Sleep, Hibernate, Shutdown, Restart}
}

// Label is the human name for an action.
func Label(a Action) string {
	switch a {
	case ExitDM:
		return "Exit ODM"
	case Sleep:
		return "Sleep"
	case Hibernate:
		return "Hibernate"
	case Shutdown:
		return "Shut down"
	case Restart:
		return "Restart"
	}
	return "Do nothing"
}

// GraceSeconds is how long shutdown and restart wait, so there is time to
// call it off with Cancel.
const GraceSeconds = 60

// Do performs the action. ExitDM is handled by the caller, since only it
// knows how to stop the daemon.
func Do(a Action) error {
	switch a {
	case None, ExitDM:
		return nil

	case Shutdown:
		// The OS timer is used rather than one of ours: Windows shows its
		// own countdown, and "shutdown /a" cancels it even if ODM is gone.
		return run("shutdown", "/s", "/t", fmt.Sprint(GraceSeconds),
			"/c", "ODM has finished downloading.")

	case Restart:
		return run("shutdown", "/r", "/t", fmt.Sprint(GraceSeconds),
			"/c", "ODM has finished downloading.")

	case Sleep:
		return suspend(false)

	case Hibernate:
		return suspend(true)
	}
	return fmt.Errorf("unknown action %q", a)
}

// CancelPending calls off a scheduled shutdown or restart.
func CancelPending() error {
	// Fails harmlessly when nothing is scheduled.
	exec.Command("shutdown", "/a").Run()
	return nil
}

// suspend puts the machine to sleep, or hibernates it.
func suspend(hibernate bool) error {
	powrprof := syscall.NewLazyDLL("powrprof.dll")
	setSuspendState := powrprof.NewProc("SetSuspendState")
	var h uintptr
	if hibernate {
		h = 1
	}
	// SetSuspendState(Hibernate, ForceCritical=0, DisableWakeEvent=0)
	r, _, err := setSuspendState.Call(h, 0, 0)
	if r == 0 {
		return fmt.Errorf("suspend: %w", err)
	}
	return nil
}

func run(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %v: %s", name, err, out)
	}
	return nil
}
