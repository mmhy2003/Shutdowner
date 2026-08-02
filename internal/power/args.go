package power

// buildShutdownArgs returns the shutdown.exe arguments for a, or nil when a is
// not an action shutdown.exe handles. Kept separate from the Windows-only code
// so the flag logic is unit-testable on any platform.
func buildShutdownArgs(a Action, force bool) []string {
	var args []string
	switch a {
	case ActionShutdown:
		args = []string{"/s", "/t", "0"}
	case ActionRestart:
		args = []string{"/r", "/t", "0"}
	default:
		return nil
	}
	if force {
		args = append(args, "/f")
	}
	return args
}
