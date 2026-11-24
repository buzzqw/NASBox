package remote

import "testing"

func TestNewestServerUpdateOnlyNewerInSameDir(t *testing.T) {
	listing := "/srv/a/sync-daemon-server-3.25.0.sh\n/srv/a/sync-daemon-server-3.27.1.sh\n" +
		"/srv/a/sync-daemon-server-3.27.0.sh\n/srv/b/sync-daemon-server-9.0.0.sh\n/srv/a/sync-daemon-server-x.sh\n"
	update := newestServerUpdate(listing, "/srv/a", "3.26.0")
	if update == nil || update.Version != "3.27.1" || update.Path != "/srv/a/sync-daemon-server-3.27.1.sh" {
		t.Fatalf("update = %+v", update)
	}
	if newestServerUpdate(listing, "/srv/a", "3.27.1") != nil {
		t.Fatal("no update must be offered when already current")
	}
}

func TestParseKeyValues(t *testing.T) {
	values := parseKeyValues("VERSION=3.27.0\nRUNNING=true\nnoise\n")
	if values["VERSION"] != "3.27.0" || values["RUNNING"] != "true" {
		t.Fatalf("%v", values)
	}
}
