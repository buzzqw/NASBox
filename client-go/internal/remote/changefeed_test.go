package remote

import "testing"

func TestParseRevisionWait(t *testing.T) {
	wait, err := ParseRevisionWait([]byte("NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\nREVISION=42\n"))
	if err != nil {
		t.Fatal(err)
	}
	if wait.Result != "CHANGED" || wait.Revision != 42 {
		t.Fatalf("wait = %+v", wait)
	}
}

func TestParseRevisionWaitRejectsMalformedResponses(t *testing.T) {
	cases := []string{
		"",
		"NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\n",
		"NASBOX_CHANGE_WAIT_V1\nRESULT=OTHER\nREVISION=1\n",
		"NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\nREVISION=x\n",
		"NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\nREVISION=-1\n",
		"NASBOX_CHANGE_WAIT_V1\nRESULT=CHANGED\nEXTRA=1\n",
		"wrong\nRESULT=CHANGED\nREVISION=1\n",
	}
	for index, input := range cases {
		if _, err := ParseRevisionWait([]byte(input)); err == nil {
			t.Fatalf("case %d accepted", index)
		}
	}
}
