package assets

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Licoy/sshyard/internal/keychain"
)

func TestRDPHostEncryptedPasswordStorage(t *testing.T) {
	dir := t.TempDir()
	metadataPath := filepath.Join(dir, "hosts.json")
	system := keychain.NewMemory()
	options := keychain.OpenOptions{
		Path: filepath.Join(dir, "secrets.enc"), LockPath: filepath.Join(dir, "secrets.lock"),
		Scope: "rdp-host-test", System: system,
	}
	vault := openRDPSecretTestStore(t, options)
	metadata := mustOpenAssets(t, metadataPath)

	host := addAssetHost(t, metadata, Host{Protocol: ProtoRDP, Address: "desktop.example", Auth: AuthPassword})
	credential, err := metadata.SaveCredential(Credential{Name: "shared password", Kind: CredPassword})
	if err != nil {
		t.Fatal(err)
	}
	shared := addAssetHost(t, metadata, Host{Protocol: ProtoRDP, Address: "shared.example", Auth: AuthCredential, CredentialID: credential.ID})
	hostSecret := newRDPSecretTestPassword(t)
	sharedSecret := newRDPSecretTestPassword(t)
	for key, secret := range map[string][]byte{
		keychain.Host(host.ID, keychain.Password):       hostSecret,
		keychain.Cred(credential.ID, keychain.Password): sharedSecret,
	} {
		if err := vault.Set(key, secret); err != nil {
			t.Fatal(err)
		}
	}
	if err := vault.Close(); err != nil {
		t.Fatal(err)
	}
	if keys := system.Keys(); !slices.Equal(keys, []string{keychain.MasterKey}) {
		t.Fatal("the system store should contain only the master key")
	}
	assertRDPSecretsAbsent(t, metadataPath, hostSecret, sharedSecret)
	assertRDPSecretsAbsent(t, options.Path, hostSecret, sharedSecret)

	metadata = mustOpenAssets(t, metadataPath)
	vault = openRDPSecretTestStore(t, options)
	assertRDPSecretRoundTrip(t, metadata, vault, host, shared, hostSecret, sharedSecret)
}

func openRDPSecretTestStore(t *testing.T, options keychain.OpenOptions) *keychain.Keychain {
	t.Helper()
	vault, err := keychain.Open(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := vault.Close(); err != nil {
			t.Error(err)
		}
	})
	return vault
}

func newRDPSecretTestPassword(t *testing.T) []byte {
	t.Helper()
	var random [24]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	secret := make([]byte, hex.EncodedLen(len(random)))
	hex.Encode(secret, random[:])
	clear(random[:])
	t.Cleanup(func() { clear(secret) })
	return secret
}

func assertRDPSecretsAbsent(t *testing.T, path string, secrets ...[]byte) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if bytes.Contains(data, secret) || bytes.Contains(data, []byte(base64.StdEncoding.EncodeToString(secret))) {
			t.Fatalf("password persisted in %s", filepath.Base(path))
		}
	}
}

func assertRDPSecretRoundTrip(t *testing.T, metadata *Store, vault *keychain.Keychain, host, shared Host, hostSecret, sharedSecret []byte) {
	t.Helper()
	saved, ok := metadata.Get(host.ID)
	if !ok || !saved.IsRDP() || saved.Auth != AuthPassword || saved.CredentialID != "" {
		t.Fatal("host-owned password metadata did not survive reopening")
	}
	assertRDPSecret(t, vault, keychain.Host(saved.ID, keychain.Password), hostSecret)
	saved, ok = metadata.Get(shared.ID)
	if !ok || !saved.IsRDP() || saved.Auth != AuthCredential || saved.CredentialID != shared.CredentialID {
		t.Fatal("shared credential reference did not survive reopening")
	}
	cred, ok := metadata.Credential(saved.CredentialID)
	if !ok || cred.Kind != CredPassword {
		t.Fatal("shared password credential metadata did not survive reopening")
	}
	assertRDPSecret(t, vault, keychain.Cred(cred.ID, keychain.Password), sharedSecret)
	if _, err := vault.Get(keychain.Host(saved.ID, keychain.Password)); !errors.Is(err, keychain.ErrNotFound) {
		t.Fatal("the shared credential must not acquire a separate host-owned password")
	}
}

func assertRDPSecret(t *testing.T, vault *keychain.Keychain, key string, want []byte) {
	t.Helper()
	got, err := vault.Get(key)
	defer clear(got)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("password readback did not match: %v", err)
	}
}
