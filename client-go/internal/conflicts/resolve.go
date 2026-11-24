package conflicts

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"sync"
)

type preparedGroup struct {
	original   string
	candidates []string
}

func prepareGroup(files *os.Root, group Group) (preparedGroup, error) {
	original, err := cleanRelative(group.Original)
	if err != nil {
		return preparedGroup{}, err
	}
	if _, err := regularFileInfo(files, original); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return preparedGroup{}, err
	}
	prepared := preparedGroup{original: original}
	seen := map[string]struct{}{}
	for _, raw := range group.Candidates {
		candidate, err := cleanRelative(raw)
		if err != nil {
			return preparedGroup{}, err
		}
		if candidate == original {
			return preparedGroup{}, fmt.Errorf("la copia di conflitto coincide con l'originale: %s", candidate)
		}
		if path.Dir(candidate) != path.Dir(original) {
			return preparedGroup{}, fmt.Errorf("la copia di conflitto non e' nella cartella dell'originale: %s", candidate)
		}
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		if _, err := regularFileInfo(files, candidate); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return preparedGroup{}, err
		}
		seen[candidate] = struct{}{}
		prepared.candidates = append(prepared.candidates, candidate)
	}
	sort.Strings(prepared.candidates)
	return prepared, nil
}

func (group preparedGroup) contains(relative string) bool {
	if relative == group.original {
		return true
	}
	for _, candidate := range group.candidates {
		if relative == candidate {
			return true
		}
	}
	return false
}

func (group preparedGroup) allCandidatesMissing(files *os.Root) bool {
	for _, candidate := range group.candidates {
		if _, err := regularFileInfo(files, candidate); err == nil {
			return false
		} else if !errors.Is(err, fs.ErrNotExist) {
			return false
		}
	}
	return true
}

// Resolve keeps chosen (the original or one candidate) and moves every losing
// version into local history. Repeating a completed resolution is a no-op.
func Resolve(root string, group Group, chosen string) error {
	files, identity, err := openRoot(root)
	if err != nil {
		return err
	}
	defer files.Close()
	prepared, err := prepareGroup(files, group)
	if err != nil {
		return err
	}
	chosen, err = cleanRelative(chosen)
	if err != nil {
		return err
	}
	if !prepared.contains(chosen) {
		return fmt.Errorf("la versione scelta non e' piu' disponibile")
	}

	_, originalErr := regularFileInfo(files, prepared.original)
	if originalErr != nil && !errors.Is(originalErr, fs.ErrNotExist) {
		return originalErr
	}
	if chosen == prepared.original {
		if originalErr != nil {
			return fmt.Errorf("la versione originale non e' piu' disponibile")
		}
		return discardCandidates(files, prepared, chosen)
	}

	chosenInfo, chosenErr := regularFileInfo(files, chosen)
	if chosenErr != nil {
		if !errors.Is(chosenErr, fs.ErrNotExist) {
			return chosenErr
		}
		// A previous invocation already promoted this candidate. An in-process
		// receipt covers partial retries; List metadata also lets a restarted
		// client recognize promotion by content.
		if originalErr == nil {
			key := resolutionKey(identity, prepared.original, chosen)
			_, promoted := resolvedCopies.Load(key)
			if promoted || prepared.allCandidatesMissing(files) || metadataShowsPromotion(files, group, prepared.original, chosen) {
				err := discardCandidates(files, prepared, chosen)
				if err == nil {
					resolvedCopies.Delete(key)
				}
				return err
			}
		}
		return fmt.Errorf("la versione scelta non e' piu' disponibile")
	}

	trashPath := ""
	if originalErr == nil {
		trashPath, err = moveToTrash(files, prepared.original)
		if err != nil {
			return fmt.Errorf("impossibile conservare l'originale: %w", err)
		}
	}
	key := resolutionKey(identity, prepared.original, chosen)
	if err := promoteVersion(files, chosen, prepared.original, chosenInfo); err != nil {
		if errors.Is(err, errPromotionCompleted) {
			resolvedCopies.Store(key, prepared.original)
			return err
		}
		if trashPath != "" {
			if restoreErr := restoreTrash(root, prepared.original, trashPath); restoreErr != nil {
				return fmt.Errorf("promozione fallita (%v); originale conservato in %s, ripristino fallito: %w", err, trashPath, restoreErr)
			}
		}
		return err
	}
	resolvedCopies.Store(key, prepared.original)
	err = discardCandidates(files, prepared, chosen)
	if err == nil {
		resolvedCopies.Delete(key)
	}
	return err
}

var resolvedCopies sync.Map // canonical root + original + chosen -> canonical path

func resolutionKey(identity, original, chosen string) string {
	return identity + "\x00" + original + "\x00" + chosen
}

func metadataShowsPromotion(files *os.Root, group Group, original, chosen string) bool {
	digest := readMetadata(files, original, "").Digest
	if digest == "" {
		return false
	}
	for _, member := range group.Members {
		if member.Path == chosen && member.Digest == digest {
			return true
		}
	}
	return false
}

func discardCandidates(files *os.Root, group preparedGroup, chosen string) error {
	for _, candidate := range group.candidates {
		if candidate == chosen {
			continue
		}
		if _, err := regularFileInfo(files, candidate); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if _, err := moveToTrash(files, candidate); err != nil {
			return fmt.Errorf("impossibile spostare nello storico %s: %w", candidate, err)
		}
	}
	return nil
}

// Discard moves every alternative version into local history and keeps the
// original untouched. Missing candidates are ignored, making retries safe.
func Discard(root string, group Group) error {
	files, _, err := openRoot(root)
	if err != nil {
		return err
	}
	defer files.Close()
	prepared, err := prepareGroup(files, group)
	if err != nil {
		return err
	}
	return discardCandidates(files, prepared, "")
}

var keptCopies sync.Map // canonical root + candidate -> archived relative path

func keptCopyKey(root, candidate string) string {
	return rootIdentity(root) + "\x00" + candidate
}

func archivedName(stem, origin, suffix string, counter int) string {
	if counter == 0 {
		return fmt.Sprintf("%s (copia archiviata %s)%s", stem, origin, suffix)
	}
	return fmt.Sprintf("%s (copia archiviata %s %d)%s", stem, origin, counter, suffix)
}

func archivedParts(candidate string) (stem, origin, suffix string) {
	if parts, ok := parseConflictName(path.Base(candidate)); ok {
		return parts.stem, parts.origin, parts.suffix
	}
	stem, suffix = splitExtension(path.Base(candidate))
	return stem, "copia", suffix
}

func conflictMemberDigest(group Group, chosen string) string {
	for _, member := range group.Members {
		if member.Path == chosen {
			return member.Digest
		}
	}
	return ""
}

func findArchivedMetadataMatch(files *os.Root, directory, stem, origin, suffix, digest string) (string, bool) {
	for counter := 2; ; counter++ {
		target := path.Join(directory, archivedName(stem, origin, suffix, counter))
		if _, err := regularFileInfo(files, target); errors.Is(err, fs.ErrNotExist) {
			return "", false
		} else if err != nil {
			return "", false
		}
		if readMetadata(files, target, "").Digest == digest {
			return target, true
		}
	}
}

// KeepBoth renames a candidate to Python's permanent "copia archiviata" name
// and leaves the original untouched. A completed call returns the same path on
// retries, including when the first archived name was already occupied.
func KeepBoth(root string, group Group, chosen string) (string, error) {
	files, identity, err := openRoot(root)
	if err != nil {
		return "", err
	}
	defer files.Close()
	prepared, err := prepareGroup(files, group)
	if err != nil {
		return "", err
	}
	chosen, err = cleanRelative(chosen)
	if err != nil {
		return "", err
	}
	if chosen == prepared.original || !prepared.contains(chosen) {
		return "", fmt.Errorf("per tenere entrambe scegli una copia conflitto")
	}
	key := keptCopyKey(identity, chosen)
	stem, origin, suffix := archivedParts(chosen)

	chosenInfo, chosenErr := regularFileInfo(files, chosen)
	if chosenErr != nil {
		if !errors.Is(chosenErr, fs.ErrNotExist) {
			return "", chosenErr
		}
		if stored, ok := keptCopies.Load(key); ok {
			target := stored.(string)
			if _, err := regularFileInfo(files, target); err != nil {
				return "", fmt.Errorf("la copia archiviata non e' piu' disponibile: %w", err)
			}
			return target, nil
		}
		// Recover the common no-collision case even after a process restart.
		target := path.Join(path.Dir(chosen), archivedName(stem, origin, suffix, 0))
		if _, err := regularFileInfo(files, target); err == nil {
			digest := conflictMemberDigest(group, chosen)
			if digest == "" || readMetadata(files, target, "").Digest == digest {
				return target, nil
			}
			if recovered, ok := findArchivedMetadataMatch(files, path.Dir(chosen), stem, origin, suffix, digest); ok {
				return recovered, nil
			}
		}
		return "", fmt.Errorf("la copia di conflitto non esiste piu'")
	}

	for counter := 0; ; {
		target := path.Join(path.Dir(chosen), archivedName(stem, origin, suffix, counter))
		if _, err := files.Lstat(target); err == nil {
			if counter == 0 {
				counter = 2
			} else {
				counter++
			}
			continue
		} else if !errors.Is(err, fs.ErrNotExist) {
			return "", err
		}
		err := promoteVersion(files, chosen, target, chosenInfo)
		if errors.Is(err, fs.ErrExist) {
			// Another process claimed the name between Lstat and Link.
			if counter == 0 {
				counter = 2
			} else {
				counter++
			}
			continue
		}
		if err != nil {
			if errors.Is(err, errPromotionCompleted) {
				keptCopies.Store(key, target)
			}
			return target, err
		}
		keptCopies.Store(key, target)
		return target, nil
	}
}
