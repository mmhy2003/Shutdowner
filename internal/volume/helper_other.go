//go:build !windows

package volume

// RunHelper is the entry point for the --audio-helper subcommand. Off Windows
// there is nothing to talk to, so it reports that in the same single-line shape
// the Windows build uses — the caller parses one format, never two.
func RunHelper([]string) string {
	return EncodeResult(State{}, ErrUnsupported)
}
