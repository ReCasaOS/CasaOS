package service

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"os/user"
	"regexp"
	"strings"
)

const (
	// sambaNologinShell is the login shell given to accounts created for sharing,
	// so they cannot be used to log in to the host at all.
	sambaNologinShell = "/usr/sbin/nologin"

	// sambaAccountComment is written to the GECOS field and is how these accounts
	// are recognised again later.
	//
	// The shell alone would not do: plenty of unrelated system accounts use
	// nologin, and this marker gates deletion. Matching too broadly there would
	// mean the API could remove a real account of the machine.
	sambaAccountComment = "CasaOS share account"
)

var (
	// sambaUsernamePattern is deliberately narrower than what useradd accepts.
	// These names reach useradd, smbpasswd and an smb.conf "valid users" line, so
	// anything outside a conservative POSIX set is rejected rather than escaped.
	sambaUsernamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,30}$`)

	ErrSambaUsernameInvalid = errors.New("a share account name must be 1 to 31 characters, start with a lowercase letter or underscore, and contain only lowercase letters, digits, underscores and hyphens")
	ErrSambaPasswordEmpty   = errors.New("a share account password cannot be empty")
	ErrSambaUserExists      = errors.New("a system account with that name already exists")
	ErrSambaUserNotManaged  = errors.New("that account was not created by CasaOS and will not be removed")
)

// ValidateSambaUsername reports whether name is safe to pass to the account
// tooling. Callers must run this before anything else touches the name.
func ValidateSambaUsername(name string) error {
	if !sambaUsernamePattern.MatchString(name) {
		return ErrSambaUsernameInvalid
	}

	return nil
}

// isCasaOSManagedAccount reports whether an account carries the marker this code
// writes when it creates one.
//
// Erring in one direction leaves a stale account behind; erring in the other
// deletes a real user of the machine. The check is written to fail towards the
// first.
func isCasaOSManagedAccount(username string) bool {
	entry, err := exec.Command("getent", "passwd", username).Output()
	if err != nil {
		return false
	}

	fields := strings.Split(strings.TrimSpace(string(entry)), ":")

	return len(fields) >= 7 && fields[4] == sambaAccountComment && fields[6] == sambaNologinShell
}

// ListSambaUsers returns the share accounts this code created.
func ListSambaUsers() ([]string, error) {
	output, err := exec.Command("getent", "passwd").Output()
	if err != nil {
		return nil, err
	}

	names := []string{}

	scanner := bufio.NewScanner(bytes.NewReader(output))
	for scanner.Scan() {
		fields := strings.Split(strings.TrimSpace(scanner.Text()), ":")
		if len(fields) < 7 || fields[4] != sambaAccountComment || fields[6] != sambaNologinShell {
			continue
		}

		names = append(names, fields[0])
	}

	return names, scanner.Err()
}

// CreateSambaUser adds a system account that can only be used for file sharing,
// then registers it with Samba.
//
// The account gets no home directory and no login shell, so it cannot be used to
// log in to the host. The password is written to the tool's standard input
// rather than passed as an argument, which keeps it out of the process table.
func CreateSambaUser(username, password string) error {
	if err := ValidateSambaUsername(username); err != nil {
		return err
	}

	if password == "" {
		return ErrSambaPasswordEmpty
	}

	if _, err := user.Lookup(username); err == nil {
		return ErrSambaUserExists
	}

	output, err := exec.Command(
		"useradd",
		"--system",
		"--no-create-home",
		"--shell", sambaNologinShell,
		"--comment", sambaAccountComment,
		username,
	).CombinedOutput()
	if err != nil {
		return fmt.Errorf("creating the system account: %s", strings.TrimSpace(string(output)))
	}

	if err := SetSambaPassword(username, password); err != nil {
		// Do not leave a POSIX account behind that Samba never learned about.
		_, _ = exec.Command("userdel", username).CombinedOutput()

		return err
	}

	return nil
}

// SetSambaPassword sets or replaces the Samba password of an existing account.
func SetSambaPassword(username, password string) error {
	if err := ValidateSambaUsername(username); err != nil {
		return err
	}

	if password == "" {
		return ErrSambaPasswordEmpty
	}

	// Without this the endpoint would happily enrol root, or any other existing
	// system account, into Samba with a password the caller chose.
	if !isCasaOSManagedAccount(username) {
		return ErrSambaUserNotManaged
	}

	// -s reads the password twice from stdin instead of prompting, and -a is a
	// no-op for an account smbpasswd already knows.
	cmd := exec.Command("smbpasswd", "-s", "-a", username)
	cmd.Stdin = strings.NewReader(password + "\n" + password + "\n")

	output, err := cmd.CombinedOutput()
	if err != nil {
		// The output can echo the account name but never the password.
		return fmt.Errorf("setting the share password: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

// DeleteSambaUser removes an account this code created, from Samba and from the
// system. It refuses to touch anything that does not look CasaOS-managed.
func DeleteSambaUser(username string) error {
	if err := ValidateSambaUsername(username); err != nil {
		return err
	}

	if _, err := user.Lookup(username); err != nil {
		return nil
	}

	if !isCasaOSManagedAccount(username) {
		return ErrSambaUserNotManaged
	}

	if output, err := exec.Command("smbpasswd", "-x", username).CombinedOutput(); err != nil {
		return fmt.Errorf("removing the samba account: %s", strings.TrimSpace(string(output)))
	}

	if output, err := exec.Command("userdel", username).CombinedOutput(); err != nil {
		return fmt.Errorf("removing the system account: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

// IsSambaShareAccount says whether username is a share account this code made:
// a member of a share may become its owner, so never root, a service account
// or a person's own login.
func IsSambaShareAccount(username string) bool {
	return isCasaOSManagedAccount(username)
}
