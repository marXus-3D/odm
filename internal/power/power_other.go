//go:build !windows

// Package power performs the "when everything is done" actions.
package power

import (
	"fmt"
	"os/exec"
)

type Action string

const (
	None      Action = "none"
	ExitDM    Action = "exit"
	Sleep     Action = "sleep"
	Hibernate Action = "hibernate"
	Shutdown  Action = "shutdown"
	Restart   Action = "restart"
)

func Valid(a Action) bool {
	switch a {
	case None, ExitDM, Sleep, Hibernate, Shutdown, Restart:
		return true
	}
	return false
}

func Actions() []Action {
	return []Action{None, ExitDM, Sleep, Hibernate, Shutdown, Restart}
}

func Label(a Action) string {
	switch a {
	case ExitDM:
		return "Exit DM"
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

const GraceSeconds = 60

// Do uses systemctl where it exists, which covers most desktop Linux.
func Do(a Action) error {
	switch a {
	case None, ExitDM:
		return nil
	case Sleep:
		return exec.Command("systemctl", "suspend").Run()
	case Hibernate:
		return exec.Command("systemctl", "hibernate").Run()
	case Shutdown:
		return exec.Command("systemctl", "poweroff").Run()
	case Restart:
		return exec.Command("systemctl", "reboot").Run()
	}
	return fmt.Errorf("unknown action %q", a)
}

func CancelPending() error { return nil }
