//go:build !linux

package agent

// copyXattrs: extended attributes are copied on Linux only (the agent's production OS).
func copyXattrs(src, dst string) string { return "" }
