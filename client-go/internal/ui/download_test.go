package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fyne.io/fyne/v2/test"
	"fyne.io/fyne/v2/theme"

	"nasbox-go/internal/config"
	"nasbox-go/internal/i18n"
	"nasbox-go/internal/remote"
	"nasbox-go/internal/transfer"
)

func newBrowseTestApp(t *testing.T) (*clientApp, string) {
	t.Helper()
	i18n.Language = "it"
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	root := t.TempDir()
	cfg := config.New("")
	cfg.Set("local_root", root, false)
	// A download that is attempted here fails at once: the test never reaches
	// the NAS, and a "Download fallito" line proves the guard did not work.
	cfg.Set("nas_host", "127.0.0.1", false)
	cfg.Set("nas_user", "nobody", false)
	cfg.Set("ssh_port", 1, false)
	client := remote.New(cfg)
	a := test.NewTempApp(t)
	window := test.NewWindow(nil)
	c := &clientApp{
		cfg: cfg, client: client, transfers: transfer.New(cfg, client),
		fyneApp: a, window: window, uiTheme: newNAStheme(theme.VariantLight), level: statusOK,
	}
	c.setHint = c.setHintText
	a.Settings().SetTheme(c.uiTheme)
	window.SetContent(c.buildBrowsePane())
	return c, root
}

// A file that is already here with the same size and date must not be
// downloaded again: the transfer used to run for nothing and left no trace.
func TestBrowseDownloadSkipsIdenticalLocalFile(t *testing.T) {
	c, root := newBrowseTestApp(t)
	name := "I CUSTODI DELLA FIAMMA (YEAR ZERO VER) (2).pdf"
	local := filepath.Join(root, name)
	if err := os.WriteFile(local, []byte("contenuto già presente"), 0o644); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	c.browseEntries = []remote.BrowseEntry{{
		Name: name, Kind: "FILE", Size: stat.Size(), MTime: stat.ModTime().Unix(),
	}}
	c.selectedBrowse = name
	c.updateBrowseSelection()

	c.onBrowseDownload()

	if c.downloadBusy {
		t.Fatal("download avviato su un file identico già presente")
	}
	if !strings.Contains(c.downloadNotice, name) {
		t.Fatalf("nessun avviso «già presente»: %q", c.downloadNotice)
	}
	if !strings.Contains(c.browseSelectedLabel.Text, name) {
		t.Fatalf("l'etichetta della scheda File non mostra l'esito: %q", c.browseSelectedLabel.Text)
	}
	for _, entry := range c.logEntries {
		if strings.Contains(entry.Detail, tr("browse.download_failed_title", nil)) {
			t.Fatalf("il download è stato tentato: %s", entry.Detail)
		}
	}
}

func TestDownloadAlreadyLocalComparesSizeAndDate(t *testing.T) {
	c, root := newBrowseTestApp(t)
	name := "file.pdf"
	local := filepath.Join(root, name)
	if err := os.WriteFile(local, []byte("quattro"), 0o644); err != nil {
		t.Fatal(err)
	}
	stat, err := os.Stat(local)
	if err != nil {
		t.Fatal(err)
	}
	c.browseEntries = []remote.BrowseEntry{{
		Name: name, Kind: "FILE", Size: stat.Size(), MTime: stat.ModTime().Unix(),
	}}
	if !c.downloadAlreadyLocal(name) {
		t.Fatal("file identico non riconosciuto")
	}
	// Un file modificato in locale (stessa data, dimensione diversa) va riscaricato.
	c.browseEntries[0].Size = stat.Size() + 1
	if c.downloadAlreadyLocal(name) {
		t.Fatal("dimensione diversa considerata uguale")
	}
	c.browseEntries[0].Size = stat.Size()
	c.browseEntries[0].MTime = stat.ModTime().Unix() - 60
	if c.downloadAlreadyLocal(name) {
		t.Fatal("data diversa considerata uguale")
	}
	// Un percorso fuori dalla cartella in elenco non viene confrontato.
	if c.downloadAlreadyLocal("altra/" + name) {
		t.Fatal("percorso fuori elenco considerato presente")
	}
}
