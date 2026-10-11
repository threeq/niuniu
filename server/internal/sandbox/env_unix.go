//go:build !windows

package sandbox

// boxEnv is the box environment. The capability layer promises the function
// cannot read the host environment, so the block is built from scratch rather
// than inherited — every entry here is a neutral placeholder, never a value
// taken from the server's own environment.
func boxEnv() []string {
	return []string{"PATH=", "HOME=", "TMPDIR=", "LANG=C", "LC_ALL=C"}
}
