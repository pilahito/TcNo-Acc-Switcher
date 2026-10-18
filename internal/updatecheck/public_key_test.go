package updatecheck

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizePublicKeyOpenSSH(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	authorized := []byte("ssh-ed25519 " + base64.StdEncoding.EncodeToString(sshWireKey("ssh-ed25519", pub)) + " updater@tcno\n")

	got := NormalizePublicKey(authorized)
	if !bytes.Equal(got, pub) {
		t.Fatalf("NormalizePublicKey = %x, want raw key %x", got, []byte(pub))
	}
}

// authorized_keys shapes OpenSSH accepts but NormalizePublicKey deliberately
// does not: they must pass through untouched rather than yield a wrong key.
func TestNormalizePublicKeyRejectsUnsupportedLines(t *testing.T) {
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	body := base64.StdEncoding.EncodeToString(sshWireKey("ssh-ed25519", pub))
	for name, line := range map[string]string{
		"comment line":  "# updater key\nssh-ed25519 " + body + "\n",
		"options":       `command="/bin/true",no-pty ssh-ed25519 ` + body + "\n",
		"wrong algo":    "ssh-rsa " + body + "\n",
		"truncated key": "ssh-ed25519 " + base64.StdEncoding.EncodeToString(sshWireKey("ssh-ed25519", pub[:16])) + "\n",
	} {
		t.Run(name, func(t *testing.T) {
			if got := NormalizePublicKey([]byte(line)); !bytes.Equal(got, []byte(line)) {
				t.Fatalf("line was parsed as %x, want passthrough", got)
			}
		})
	}
}

func sshWireKey(algo string, key []byte) []byte {
	blob := binary.BigEndian.AppendUint32(nil, uint32(len(algo)))
	blob = append(blob, algo...)
	blob = binary.BigEndian.AppendUint32(blob, uint32(len(key)))
	return append(blob, key...)
}

func TestNormalizePublicKeyPassthrough(t *testing.T) {
	raw := make([]byte, ed25519.PublicKeySize)
	if got := NormalizePublicKey(raw); !bytes.Equal(got, raw) {
		t.Fatalf("raw key changed: %x", got)
	}
	garbage := []byte("not a key")
	if got := NormalizePublicKey(garbage); !bytes.Equal(got, garbage) {
		t.Fatalf("garbage changed: %x", got)
	}
}

// The embedded updater-key.pub must normalize to a raw ed25519 key, or the
// Wails updater rejects every signed release at verify time.
func TestEmbeddedUpdaterKeyNormalizes(t *testing.T) {
	got := NormalizePublicKey(readUpdaterKey(t))
	if len(got) != ed25519.PublicKeySize {
		t.Fatalf("normalized key is %d bytes, want %d; updater cannot use it", len(got), ed25519.PublicKeySize)
	}
}

// Known-answer vector for this fork's updater key (pilahito/TcNo-Acc-Switcher,
// rotated away from TCNOco's key): sha256 of the string
// "pilahito/TcNo-Acc-Switcher updater key known-answer vector v1" signed with
// the private key held in the fork's UPDATER_KEY Actions secret. Proves
// updater-key.pub matches that secret, not just that it parses. If this fails
// after a deliberate key rotation, re-pin the vector with the new key.
func TestEmbeddedUpdaterKeyMatchesReleaseSignature(t *testing.T) {
	digest, err := hex.DecodeString("434737f12da93f80800a008372502fc0e7ba914c30e7a40df8a09b033d270336")
	if err != nil {
		t.Fatal(err)
	}
	sig, err := base64.StdEncoding.DecodeString("RQE2GxwhOUoePF6Plf+8uKP8r+7IjUx1qvm57LbYc8kedyKK6S8Xy692gUZ2syMxN97fSeQ6F9a9V/R9CvTmAQ==")
	if err != nil {
		t.Fatal(err)
	}

	key := NormalizePublicKey(readUpdaterKey(t))
	if len(key) != ed25519.PublicKeySize {
		t.Fatalf("normalized key is %d bytes, want %d", len(key), ed25519.PublicKeySize)
	}
	if !ed25519.Verify(ed25519.PublicKey(key), digest, sig) {
		t.Fatal("known-answer signature does not verify against updater-key.pub; released updates will fail to install")
	}
}

func readUpdaterKey(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "updater-key.pub"))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
