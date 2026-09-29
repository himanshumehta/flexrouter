//go:build darwin

package secrets

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

func platformStore() Store { return keychain{} }

// keychain shells out to /usr/bin/security. Secrets are passed on stdin via
// `security -i` so they never appear in the process list.
type keychain struct{}

func (keychain) Get(account string) (string, error) {
	out, err := exec.Command("/usr/bin/security", "find-generic-password", "-s", Service, "-a", account, "-w").Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("keychain read failed: %w", err)
	}
	return strings.TrimRight(string(out), "\n"), nil
}

func (keychain) Set(account, label, secret string) error {
	for _, v := range []string{account, label, secret} {
		if strings.ContainsAny(v, "\"\\\n\r") {
			return fmt.Errorf("value contains characters the Keychain helper cannot pass safely")
		}
	}
	cmd := exec.Command("/usr/bin/security", "-i")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("add-generic-password -U -s %q -a %q -l %q -w %q\n", Service, account, label, secret))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("keychain write failed: %v", err)
	}
	if strings.Contains(stderr.String(), "error") {
		return fmt.Errorf("keychain write failed")
	}
	return nil
}

func (keychain) Delete(account string) error {
	err := exec.Command("/usr/bin/security", "delete-generic-password", "-s", Service, "-a", account).Run()
	if ee, ok := err.(*exec.ExitError); ok && ee.ExitCode() == 44 {
		return nil
	}
	return err
}

func (keychain) Check() error {
	return exec.Command("/usr/bin/security", "show-keychain-info").Run()
}
