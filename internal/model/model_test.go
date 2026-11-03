package model

import "testing"

func TestValidID(t *testing.T) {
	for _, ok := range []string{"dQw4w9WgXcQ", "jNQXAC9IVRw", "-_-_-_-_-_-", "AAAAAAAAAAA"} {
		if !ValidID(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", "short", "dQw4w9WgXcQ1", "../../a/b/c", "dQw4w9WgXc%", "dQw4w9WgXc ", "dQw4w9WgXc\x00", "dQw4w9WgXcé", "-o%(x)s__a"} {
		if ValidID(bad) {
			t.Errorf("%q should be rejected", bad)
		}
	}
}
