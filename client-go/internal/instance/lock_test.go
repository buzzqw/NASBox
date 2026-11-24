package instance

import "testing"

func TestSecondInstanceIsRejected(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	release, err := Acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Acquire(); err == nil {
		t.Fatal("second instance must be rejected")
	}
}
