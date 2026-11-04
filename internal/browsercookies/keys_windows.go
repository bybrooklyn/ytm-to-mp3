//go:build windows

package browsercookies

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// winKeys reads the AES-256 key from the browser's "Local State" file, unwrapped with
// DPAPI (it only works for the user who owns the browser profile).
type winKeys struct {
	root   string
	cached []key
	loaded bool
}

func newKeySource(_ *chromiumDef, root string) keySource { return &winKeys{root: root} }

func (w *winKeys) keys(string) []key {
	if !w.loaded {
		w.loaded = true
		if b, err := os.ReadFile(filepath.Join(w.root, "Local State")); err == nil {
			if blob := localStateKeyBlob(b); blob != nil {
				if k := dpapiUnprotect(blob); len(k) == 32 {
					w.cached = []key{k}
				}
			}
		}
	}
	return w.cached
}

// localStateKeyBlob: os_crypt.encrypted_key is base64("DPAPI" + a DPAPI blob).
func localStateKeyBlob(js []byte) []byte {
	var ls struct {
		OSCrypt struct {
			EncryptedKey string `json:"encrypted_key"`
		} `json:"os_crypt"`
	}
	if json.Unmarshal(js, &ls) != nil {
		return nil
	}
	raw, err := base64.StdEncoding.DecodeString(ls.OSCrypt.EncryptedKey)
	if err != nil || len(raw) < 5 || string(raw[:5]) != "DPAPI" {
		return nil
	}
	return raw[5:]
}

func dpapiUnprotect(blob []byte) []byte {
	in := windows.DataBlob{Size: uint32(len(blob)), Data: &blob[0]}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, 0, &out); err != nil {
		return nil
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	return append([]byte(nil), unsafe.Slice(out.Data, out.Size)...)
}
