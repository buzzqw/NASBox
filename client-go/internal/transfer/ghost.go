package transfer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"nasbox-go/internal/config"
	"nasbox-go/internal/paths"
	"nasbox-go/internal/remote"
)

// DownloadRemotePath fetches one remote path into the configured local folder.
// Directories are walked with --browse-list and downloaded file by file: the
// chunk backend has a file-level protocol, while rsync can already recurse.
// Walking in the client makes both backends behave the same way.
func DownloadRemotePath(client *remote.Client, backend Backend, relative string) error {
	relative = strings.Trim(strings.TrimSpace(relative), "/")
	if relative == "" {
		return fmt.Errorf("percorso remoto vuoto")
	}
	entries, err := client.BrowseList(relative)
	if err != nil {
		// Not a directory (or not browsable): let the backend fetch the file.
		return backend.DownloadPath(relative)
	}
	return downloadBrowseDir(client, backend, relative, entries)
}

func downloadBrowseDir(client *remote.Client, backend Backend, dir string, entries []remote.BrowseEntry) error {
	for _, entry := range entries {
		rel := entry.Name
		if dir != "" {
			rel = dir + "/" + entry.Name
		}
		switch entry.Kind {
		case "DIR":
			children, err := client.BrowseList(rel)
			if err != nil {
				return err
			}
			if err := downloadBrowseDir(client, backend, rel, children); err != nil {
				return err
			}
		case "FILE":
			if err := backend.DownloadPath(rel); err != nil {
				return err
			}
		}
	}
	return nil
}

// LocalUpload is one local file and the NAS path it should be published to.
type LocalUpload struct {
	Local, Remote string
}

type stagedUpload struct {
	local     string
	backup    string
	createdAt string
}

// UploadLocalFiles publishes arbitrary local files to the NAS without a sync
// pass. Files are staged under the configured local folder so the same
// journaled create/replace transactions used by the engine are reused: new
// paths are published with "create", existing ones with a preconditioned
// "replace" that keeps the previous version in the NAS trash.
func UploadLocalFiles(cfg *config.Config, client *remote.Client, backend Backend, device string, items []LocalUpload) error {
	if len(items) == 0 {
		return nil
	}
	root := cfg.LocalRoot()
	if strings.TrimSpace(root) == "" {
		return fmt.Errorf("nessuna cartella di download/caricamento configurata: scegli una cartella in Impostazioni")
	}
	if err := client.ValidateHostKeyPin(context.Background()); err != nil {
		return err
	}
	device = strings.TrimSpace(device)
	if device == "" {
		device = "ghost"
	}
	repo := cfg.RepositoryID()

	remotePaths := make([]string, 0, len(items))
	for _, item := range items {
		rel := strings.Trim(strings.TrimSpace(item.Remote), "/")
		if err := paths.ValidateRelative(rel); err != nil {
			return fmt.Errorf("percorso NAS non valido %q: %w", rel, err)
		}
		info, err := os.Stat(item.Local)
		if err != nil {
			return fmt.Errorf("file locale %s: %w", item.Local, err)
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("non è un file regolare: %s", item.Local)
		}
		remotePaths = append(remotePaths, rel)
	}

	before, err := client.FileStatesCurrent(remotePaths)
	if err != nil {
		return fmt.Errorf("stato NAS non leggibile: %w", err)
	}

	staged := []stagedUpload{}
	defer func() {
		for i := len(staged) - 1; i >= 0; i-- {
			item := staged[i]
			_ = os.Remove(item.local)
			if item.backup != "" {
				_ = os.Rename(item.backup, item.local)
			}
			if item.createdAt != "" {
				pruneEmptyParents(root, item.createdAt)
			}
		}
	}()

	creates := []remote.StagedFile{}
	replaces := []remote.ReplaceFile{}
	for index, item := range items {
		rel := remotePaths[index]
		digest, size, mtime, _, err := chunkFile(item.Local, 1024*1024)
		if err != nil {
			return err
		}
		localDest, err := paths.SafeDestination(root, rel)
		if err != nil {
			return err
		}
		record := stagedUpload{local: localDest}
		if _, err := os.Lstat(localDest); err == nil {
			record.backup = localDest + ".nasbox-ghost-" + strconv.FormatInt(time.Now().UnixNano(), 10)
			if err := os.Rename(localDest, record.backup); err != nil {
				return fmt.Errorf("non posso spostare la copia locale di %s: %w", rel, err)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(localDest), 0o755); err != nil {
			return err
		}
		record.createdAt = filepath.Dir(localDest)
		if err := copyFile(item.Local, localDest); err != nil {
			return err
		}
		info, err := os.Stat(item.Local)
		if err == nil {
			_ = os.Chtimes(localDest, time.Now(), info.ModTime())
		}
		staged = append(staged, record)

		file := remote.StagedFile{Path: rel, Digest: digest, Causal: device + ":1", Size: size, MTimeNS: mtime}
		if state, ok := before[rel]; ok && state.Kind == "FILE" {
			replaces = append(replaces, remote.ReplaceFile{
				StagedFile: file, ExpectedDigest: state.Digest, ExpectedSize: state.Size,
			})
		} else {
			creates = append(creates, file)
		}
	}

	if len(creates) > 0 {
		if err := backend.UploadStaged(nil, device, creates); err != nil {
			return fmt.Errorf("caricamento nuovi file non riuscito: %w", err)
		}
	}
	if len(replaces) > 0 {
		result, err := backend.UploadReplace(replaces, device, repo, remote.RunTimestamp())
		if err != nil {
			return fmt.Errorf("sostituzione file non riuscita: %w", err)
		}
		if len(result.Stale) > 0 {
			return fmt.Errorf("file cambiati sul NAS prima della pubblicazione, riprova: %s", strings.Join(result.Stale, ", "))
		}
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// pruneEmptyParents removes the directories created only to stage an upload,
// without ever touching a directory that contains something else.
func pruneEmptyParents(root, dir string) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return
	}
	current := dir
	for {
		clean := filepath.Clean(current)
		if clean == rootAbs || !strings.HasPrefix(clean, rootAbs+string(filepath.Separator)) {
			return
		}
		entries, err := os.ReadDir(clean)
		if err != nil || len(entries) > 0 {
			return
		}
		if err := os.Remove(clean); err != nil {
			return
		}
		current = filepath.Dir(clean)
	}
}
