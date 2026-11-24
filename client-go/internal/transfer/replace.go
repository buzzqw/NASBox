package transfer

import (
	"context"
	"fmt"
	"strings"

	"nasbox-go/internal/remote"
)

// replaceRemote is the subset of the NAS client used by UploadReplace.
type replaceRemote interface {
	AcquireLease(device string) (func(), error)
	FileStatesCurrent(paths []string) (map[string]remote.FileState, error)
	JournalAppendOwned(repositoryID, txid, device string, puts []remote.JournalPut) error
	ManifestDigest(path string) (string, bool, error)
}

// trashDirName is the NAS history directory shared with the Python client
// and the server (--history-list, retention).
const trashDirName = ".sync-trash"

// UploadReplace overwrites existing NAS files the same way the Python client
// does, because staging-publish never overwrites:
//
//  1. hold the NAS lease for the whole operation;
//  2. re-read the live NAS state and skip files that changed since planning;
//  3. rsync directly, keeping the previous version in .sync-trash with the
//     run timestamp as suffix (NAS history, restorable);
//  4. read back the NAS state and journal exactly what is now on disk.
func (t *Transfer) UploadReplace(files []remote.ReplaceFile, device, repositoryID, runTS string) (remote.ReplaceResult, error) {
	result := remote.ReplaceResult{}
	if len(files) == 0 {
		return result, nil
	}
	nas := t.replaceRemote()
	if t.client != nil {
		if err := t.client.ValidateHostKeyPin(context.Background()); err != nil {
			return result, err
		}
	}
	release, err := nas.AcquireLease(device)
	if err != nil {
		return result, err
	}
	defer release()

	paths := make([]string, 0, len(files))
	for _, file := range files {
		paths = append(paths, file.Path)
	}
	before, err := nas.FileStatesCurrent(paths)
	if err != nil {
		return result, fmt.Errorf("stato NAS non leggibile prima della sostituzione: %w", err)
	}
	ready := make([]remote.ReplaceFile, 0, len(files))
	for _, file := range files {
		current, ok := before[file.Path]
		if !ok || current.Kind != "FILE" || current.Digest != file.ExpectedDigest || current.Size != file.ExpectedSize {
			result.Stale = append(result.Stale, file.Path)
			continue
		}
		ready = append(ready, file)
	}
	if err := t.adoptOutOfBand(nas, files, before, &result, device, repositoryID, runTS); err != nil {
		return result, err
	}
	if len(ready) == 0 {
		return result, nil
	}
	readyPaths := make([]string, 0, len(ready))
	for _, file := range ready {
		readyPaths = append(readyPaths, file.Path)
	}
	args, payload := t.BuildCommand(readyPaths, true)
	backupDir := strings.TrimSuffix(t.cfg.RemotePrefix(), "/") + "/" + trashDirName
	backup := []string{"--backup", "--backup-dir=" + backupDir, "--suffix=-" + runTS}
	// Backup options go before the endpoints (the last two arguments).
	args = append(args[:len(args)-2:len(args)-2], append(backup, args[len(args)-2:]...)...)
	if _, err := t.runObserved(t.ctx, payload, true, len(readyPaths), args); err != nil {
		return result, err
	}

	// Journal what the NAS really holds now. If the local file changed while
	// rsync read it, the NAS digest differs from the planned one: it is still
	// journaled truthfully and the next pass uploads the newer local bytes.
	after, err := nas.FileStatesCurrent(readyPaths)
	if err != nil {
		return result, fmt.Errorf("stato NAS non leggibile dopo la sostituzione: %w", err)
	}
	puts := make([]remote.JournalPut, 0, len(ready))
	for _, file := range ready {
		state, ok := after[file.Path]
		if !ok || state.Kind != "FILE" {
			return result, fmt.Errorf("file assente sul NAS dopo il caricamento: %s", file.Path)
		}
		put := remote.JournalPut{
			Path: file.Path, Digest: state.Digest, Size: state.Size,
			MTimeNS: state.MTimeNS, Causal: file.Causal,
		}
		if state.Digest == file.Digest && state.Size == file.Size {
			// Same bytes: keep the local sub-second mtime, as staging does.
			put.MTimeNS = file.MTimeNS
		}
		puts = append(puts, put)
	}
	if err := nas.JournalAppendOwned(repositoryID, "replace-"+runTS, device, puts); err != nil {
		return result, fmt.Errorf("journal NAS non aggiornato dopo la sostituzione: %w", err)
	}
	result.Replaced = puts
	return result, nil
}

func (t *Transfer) replaceRemote() replaceRemote {
	if t.replaceClient != nil {
		return t.replaceClient
	}
	return t.client
}

// adoptOutOfBand journals NAS files that were changed outside NASBox (the
// manifest does not know their content). Without this a local edit of such a
// file would be skipped as "stale" on every pass, forever. After adoption the
// next pass sees a normal remote change and applies the conflict policy; the
// losing version is kept (NAS history or conflict copy).
func (t *Transfer) adoptOutOfBand(
	nas replaceRemote, files []remote.ReplaceFile, before map[string]remote.FileState,
	result *remote.ReplaceResult, device, repositoryID, runTS string,
) error {
	stale := make(map[string]bool, len(result.Stale))
	for _, path := range result.Stale {
		stale[path] = true
	}
	puts := make([]remote.JournalPut, 0)
	for _, file := range files {
		current, ok := before[file.Path]
		if !stale[file.Path] || !ok || current.Kind != "FILE" {
			continue
		}
		digest, known, err := nas.ManifestDigest(file.Path)
		if err != nil {
			return fmt.Errorf("manifest NAS non leggibile per %s: %w", file.Path, err)
		}
		if known && digest == current.Digest {
			continue // another PC already journaled this version: a normal race
		}
		puts = append(puts, remote.JournalPut{
			Path: file.Path, Digest: current.Digest, Size: current.Size, MTimeNS: current.MTimeNS,
		})
		result.Adopted = append(result.Adopted, file.Path)
	}
	if len(puts) == 0 {
		return nil
	}
	return nas.JournalAppendOwned(repositoryID, "adopt-"+runTS, device, puts)
}
