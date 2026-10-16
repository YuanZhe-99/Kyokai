//go:build !windows

package localapi

// CurrentSID is empty outside Windows; identities are uids there.
func CurrentSID() string { return "" }
