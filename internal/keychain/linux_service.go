package keychain

import "strings"

// LinuxServiceErrorKind identifies only causes retained by MyGo's Linux backend.
// Unrecognized or stripped GError messages remain LinuxServiceUnknown.
type LinuxServiceErrorKind uint8

const (
	LinuxServiceUnknown LinuxServiceErrorKind = iota
	LinuxServiceDependency
	LinuxServiceSessionBus
	LinuxServiceAccessDenied
	LinuxServiceStartFailed
	LinuxServiceLocked
	// LinuxServiceMissing is a session bus without a Secret Service.
	LinuxServiceMissing
	// LinuxServiceTimeout is a Secret Service that did not answer.
	LinuxServiceTimeout
)

// LinuxServiceError preserves the original error for errors.Is and errors.As.
// Kind is diagnostic only and never authorizes creating a new master key.
type LinuxServiceError struct {
	Kind LinuxServiceErrorKind
	Err  error
}

func (e *LinuxServiceError) Error() string { return e.Err.Error() }
func (e *LinuxServiceError) Unwrap() error { return e.Err }

// MyGo (v0.9.1) discards GError domain/code, so only its own loader/bus
// messages and complete retained GDBus remote error names are reliable.
func mapLinuxServiceError(err error) error {
	message, ok := strings.CutPrefix(err.Error(), "mygo: secrets: ")
	if !ok {
		return err
	}
	noStore := false
	if rest, ok := strings.CutPrefix(message, "no credential store: "); ok {
		message, noStore = rest, true
	}
	if !noStore && !strings.HasPrefix(message, "secret service: ") {
		return err
	}
	return &LinuxServiceError{Kind: linuxServiceKind(message, noStore), Err: err}
}

func linuxServiceKind(message string, noStore bool) LinuxServiceErrorKind {
	if noStore {
		if strings.HasPrefix(message, "no session bus: ") {
			return LinuxServiceSessionBus
		}
		if message == "libsecret is too old: secrets need libsecret 0.19 or later" {
			return LinuxServiceDependency
		}
		for _, lib := range []string{"libsecret-1.so.0", "libglib-2.0.so.0", "libgio-2.0.so.0"} {
			if strings.HasPrefix(message, "cannot load "+lib+": ") {
				return LinuxServiceDependency
			}
		}
	}
	// GLib g_dbus_error_new_for_dbus_error emits GDBus.Error:<name>: <message>;
	// libsecret may strip this prefix, in which case the cause stays unknown.
	remote, ok := strings.CutPrefix(message, "secret service: GDBus.Error:")
	if !ok {
		// MyGo reports a bus without the Secret Service from the error's
		// domain and code, which survive libsecret stripping the name.
		if noStore && strings.HasPrefix(message, "secret service: ") {
			return LinuxServiceMissing
		}
		return startServiceKind(message)
	}
	name, _, ok := strings.Cut(remote, ": ")
	if !ok {
		return LinuxServiceUnknown
	}
	switch name {
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner", "org.freedesktop.DBus.Error.NoServer":
		if noStore {
			return LinuxServiceMissing
		}
	}
	return remoteKind(name)
}

// startServiceKind is a failed activation of the Secret Service: GDBusProxy
// prefixes its remote error with a translated "Error calling
// StartServiceByName for org.freedesktop.secrets: ", which names the method.
func startServiceKind(message string) LinuxServiceErrorKind {
	i := strings.LastIndex(message, ": GDBus.Error:")
	if i < 0 || !strings.Contains(message[:i], "StartServiceByName") {
		return LinuxServiceUnknown
	}
	name, _, ok := strings.Cut(message[i+len(": GDBus.Error:"):], ": ")
	if !ok {
		return LinuxServiceUnknown
	}
	if kind := remoteKind(name); kind == LinuxServiceStartFailed || kind == LinuxServiceTimeout {
		return kind
	}
	return LinuxServiceUnknown
}

func remoteKind(name string) LinuxServiceErrorKind {
	switch name {
	case "org.freedesktop.DBus.Error.AccessDenied", "org.freedesktop.DBus.Error.AuthFailed":
		return LinuxServiceAccessDenied
	case "org.freedesktop.Secret.Error.IsLocked":
		return LinuxServiceLocked
	case "org.freedesktop.DBus.Error.ServiceUnknown", "org.freedesktop.DBus.Error.NameHasNoOwner", "org.freedesktop.DBus.Error.NoServer",
		"org.freedesktop.DBus.Error.Spawn.ExecFailed", "org.freedesktop.DBus.Error.Spawn.ForkFailed",
		"org.freedesktop.DBus.Error.Spawn.ChildExited", "org.freedesktop.DBus.Error.Spawn.ChildSignaled",
		"org.freedesktop.DBus.Error.Spawn.Failed", "org.freedesktop.DBus.Error.Spawn.FailedToSetup",
		"org.freedesktop.DBus.Error.Spawn.ConfigInvalid", "org.freedesktop.DBus.Error.Spawn.ServiceNotValid",
		"org.freedesktop.DBus.Error.Spawn.ServiceNotFound", "org.freedesktop.DBus.Error.Spawn.PermissionsInvalid",
		"org.freedesktop.DBus.Error.Spawn.FileInvalid", "org.freedesktop.DBus.Error.Spawn.NoMemory":
		return LinuxServiceStartFailed
	case "org.freedesktop.DBus.Error.NoReply", "org.freedesktop.DBus.Error.TimedOut", "org.freedesktop.DBus.Error.Timeout":
		return LinuxServiceTimeout
	}
	return LinuxServiceUnknown
}
