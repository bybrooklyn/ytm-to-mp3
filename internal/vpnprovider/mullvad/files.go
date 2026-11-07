package mullvad

import "os"

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }

func removeFile(p string) error {
	err := os.Remove(p)
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
