package devices

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrAuth means the host answered but refused the client's key, which is the
// one failure `device add` can fix by asking for the account password.
var ErrAuth = errors.New("authentication failed")

// ErrPlutoMissing means the host accepted the login but has no pluto binary
// on the PATH a non-interactive ssh sees or at ~/.local/bin/pluto.
var ErrPlutoMissing = errors.New("pluto is not installed on the host")

// PublicKey returns the client's default ssh public key, generating an
// ed25519 keypair at ~/.ssh/id_ed25519 when the user has none. The key is
// unencrypted because the client runs ssh with BatchMode=yes and cannot answer
// a passphrase prompt; file permissions are its protection.
func PublicKey() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("find home directory: %w", err)
	}
	dir := filepath.Join(home, ".ssh")
	for _, name := range []string{"id_ed25519.pub", "id_ecdsa.pub", "id_rsa.pub"} {
		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err == nil {
			if key := strings.TrimSpace(string(data)); key != "" {
				return key, nil
			}
			continue
		}
		if !os.IsNotExist(err) {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
	}
	keyPath := filepath.Join(dir, "id_ed25519")
	if _, err := os.Stat(keyPath); err == nil {
		return publicFromPrivate(keyPath)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s: %w", dir, err)
	}
	return generateKey(keyPath)
}

// publicFromPrivate recovers the .pub half of an existing private key.
func publicFromPrivate(keyPath string) (string, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", keyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return "", fmt.Errorf("read %s: %w (add its .pub next to it, or remove it and retry)", keyPath, err)
	}
	pub := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
	if err := os.WriteFile(keyPath+".pub", []byte(pub+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("write %s.pub: %w", keyPath, err)
	}
	return pub, nil
}

// generateKey writes an unencrypted ed25519 keypair in the OpenSSH format the
// system ssh reads from ~/.ssh.
func generateKey(keyPath string) (string, error) {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", fmt.Errorf("generate key: %w", err)
	}
	block, err := ssh.MarshalPrivateKey(priv, "pluto")
	if err != nil {
		return "", fmt.Errorf("encode key: %w", err)
	}
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(block), 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", keyPath, err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		return "", fmt.Errorf("encode public key: %w", err)
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(pub)))
	if err := os.WriteFile(keyPath+".pub", []byte(authorized+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("write %s.pub: %w", keyPath, err)
	}
	return authorized, nil
}

// remoteLocate prints the absolute path of the host's pluto binary. It checks
// the PATH a non-interactive ssh session gets first, then the install
// location the host CLI docs use. A missing binary exits 127, which the caller
// reports as ErrPlutoMissing.
const remoteLocate = `if command -v pluto >/dev/null 2>&1; then command -v pluto; elif [ -x "$HOME/.local/bin/pluto" ]; then printf '%s\n' "$HOME/.local/bin/pluto"; else exit 127; fi`

// Locate authenticates to target and returns the absolute path of pluto
// there. It runs over the same non-interactive boundary as every command, so
// success means later commands will work without a password.
func Locate(ctx context.Context, target string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ssh", append(HostOptions(5*time.Second), "--", target, remoteLocate)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if err != nil {
		line := lastLine(stderr.String())
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 127 {
			return "", fmt.Errorf("%w (%s)", ErrPlutoMissing, target)
		}
		if strings.Contains(strings.ToLower(line), "permission denied") {
			return "", fmt.Errorf("%w: %s", ErrAuth, line)
		}
		if line != "" {
			return "", fmt.Errorf("ssh %s: %s", target, line)
		}
		return "", fmt.Errorf("ssh %s: %w", target, err)
	}
	path := strings.TrimSpace(stdout.String())
	if !validRemotePath(path) {
		return "", fmt.Errorf("ssh %s: no absolute pluto path in output %q", target, path)
	}
	return path, nil
}

// InstallKey authorizes pubKey on target using password authentication. The
// key arrives on stdin, which ssh forwards to the remote command, while ssh
// reads the password from the terminal: the classic ssh-copy-id shape. The
// remote script is static and de-duplicates, so a retry does not append the
// key twice.
func InstallKey(target, pubKey string, stdout, stderr io.Writer) error {
	const script = `umask 077; mkdir -p "$HOME/.ssh"; chmod 700 "$HOME/.ssh"; touch "$HOME/.ssh/authorized_keys"; chmod 600 "$HOME/.ssh/authorized_keys"; key=$(cat); grep -qxF "$key" "$HOME/.ssh/authorized_keys" || printf '%s\n' "$key" >> "$HOME/.ssh/authorized_keys"`
	cmd := exec.Command("ssh",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ConnectTimeout=10",
		"--", target, script)
	cmd.Stdin = strings.NewReader(pubKey + "\n")
	var complaint bytes.Buffer
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, &complaint)
	if err := cmd.Run(); err != nil {
		if line := lastLine(complaint.String()); line != "" {
			return fmt.Errorf("ssh %s: %s", target, line)
		}
		return fmt.Errorf("ssh %s: %w", target, err)
	}
	return nil
}
