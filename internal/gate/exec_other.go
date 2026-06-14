//go:build !linux

package gate

func mountIsNoexec(string) bool { return false }
