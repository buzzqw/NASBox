package conflicts

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

var errPromotionCompleted = errors.New("versione promossa ma copia sorgente ancora presente")

func cleanRelative(relative string) (string, error) {
	if relative == "" || strings.ContainsRune(relative, 0) {
		return "", fmt.Errorf("percorso vuoto o non valido")
	}
	native := filepath.FromSlash(relative)
	if filepath.IsAbs(native) || path.IsAbs(filepath.ToSlash(relative)) || filepath.VolumeName(native) != "" {
		return "", fmt.Errorf("percorso assoluto non consentito: %s", relative)
	}
	for _, component := range strings.Split(filepath.ToSlash(relative), "/") {
		if component == ".." {
			return "", fmt.Errorf("percorso fuori dalla cartella NASBox: %s", relative)
		}
	}
	cleaned := filepath.Clean(native)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("percorso fuori dalla cartella NASBox: %s", relative)
	}
	return filepath.ToSlash(cleaned), nil
}

func openRoot(root string) (*os.Root, string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, "", err
	}
	files, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, "", err
	}
	return files, rootIdentity(absolute), nil
}

// checkNoSymlinks rejects links in every component. Together with os.Root this
// keeps mutations inside NASBox even if a path is swapped during an operation.
func checkNoSymlinks(files *os.Root, relative string) error {
	components := strings.Split(relative, "/")
	current := ""
	for index, component := range components {
		if component == "" || component == "." {
			continue
		}
		current = path.Join(current, component)
		info, err := files.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("collegamento simbolico non consentito: %s", current)
		}
		if index < len(components)-1 && !info.IsDir() {
			return fmt.Errorf("componente non directory nel percorso: %s", current)
		}
	}
	return nil
}

func regularFileInfo(files *os.Root, relative string) (os.FileInfo, error) {
	if err := checkNoSymlinks(files, relative); err != nil {
		return nil, fmt.Errorf("percorso non sicuro %s: %w", relative, err)
	}
	info, err := files.Lstat(relative)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("non e' un file regolare: %s", relative)
	}
	return info, nil
}

func createCopy(root, originalRelative, destinationRelative string) (string, error) {
	original, err := cleanRelative(originalRelative)
	if err != nil {
		return "", err
	}
	destination, err := cleanRelative(destinationRelative)
	if err != nil {
		return "", err
	}
	if original == destination || path.Dir(original) != path.Dir(destination) {
		return "", fmt.Errorf("la copia di conflitto deve essere nella cartella dell'originale")
	}
	files, _, err := openRoot(root)
	if err != nil {
		return "", err
	}
	defer files.Close()

	sourceInfo, err := regularFileInfo(files, original)
	if err != nil {
		return "", err
	}
	if err := checkNoSymlinks(files, path.Dir(destination)); err != nil && path.Dir(destination) != "." {
		return "", fmt.Errorf("cartella di destinazione non sicura: %w", err)
	}
	if info, err := files.Lstat(destination); err == nil {
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("la destinazione non e' un file regolare: %s", destination)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}

	source, err := files.Open(original)
	if err != nil {
		return "", err
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, openedInfo) {
		return "", fmt.Errorf("il file originale e' cambiato durante la copia")
	}

	temporary, temporaryName, err := createTemporary(files, path.Dir(destination))
	if err != nil {
		return "", err
	}
	committed := false
	defer func() {
		temporary.Close()
		if !committed {
			files.Remove(temporaryName)
		}
	}()
	if err := copyRegularFile(source, temporary); err != nil {
		return "", err
	}
	if err := temporary.Chmod(sourceInfo.Mode().Perm()); err != nil {
		return "", err
	}
	if err := files.Chtimes(temporaryName, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := files.Rename(temporaryName, destination); err != nil {
		return "", err
	}
	committed = true
	return destination, nil
}

func createTemporary(files *os.Root, directory string) (*os.File, string, error) {
	for {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return nil, "", err
		}
		name := path.Join(directory, ".nasbox-conflict-"+hex.EncodeToString(random[:])+".tmp")
		file, err := files.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		return file, name, err
	}
}

func copyRegularFile(source io.Reader, destination io.Writer) error {
	if _, err := io.Copy(destination, source); err != nil {
		return err
	}
	return nil
}

func newRunTS() string {
	now := time.Now().UTC()
	return now.Format("2006-01-02") + "--" +
		strings.Replace(now.Format("15-04-05.000000"), ".", "-", 1) + "Z"
}

func localTrashDir() (string, error) {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sync-daemon", "trash"), nil
}

func moveToTrash(files *os.Root, relative string) (string, error) {
	relative, err := cleanRelative(relative)
	if err != nil {
		return "", err
	}
	info, err := regularFileInfo(files, relative)
	if err != nil {
		return "", err
	}
	trashDir, err := localTrashDir()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(trashDir, 0o700); err != nil {
		return "", err
	}
	trashDir, err = filepath.EvalSymlinks(trashDir)
	if err != nil {
		return "", err
	}
	destinationDir := filepath.Join(trashDir, filepath.Dir(filepath.FromSlash(relative)))
	if err := os.MkdirAll(destinationDir, 0o700); err != nil {
		return "", err
	}
	destination := filepath.Join(destinationDir, filepath.Base(relative)+"-"+newRunTS())
	for index := 1; ; index++ {
		if _, err := os.Lstat(destination); errors.Is(err, fs.ErrNotExist) {
			break
		} else if err != nil {
			return "", err
		}
		destination = filepath.Join(destinationDir, fmt.Sprintf("%s-%s-%d", filepath.Base(relative), newRunTS(), index))
	}

	sourcePath := filepath.Join(files.Name(), filepath.FromSlash(relative))
	if err := os.Rename(sourcePath, destination); err == nil {
		return destination, nil
	} else if !errors.Is(err, syscall.EXDEV) {
		return "", err
	}

	// Local history can be on another filesystem. Copy and verify before
	// removing the source, and remove the incomplete copy if the source cannot
	// be removed afterwards.
	source, err := files.Open(relative)
	if err != nil {
		return "", err
	}
	defer source.Close()
	openedInfo, err := source.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		return "", fmt.Errorf("il file da conservare e' cambiato durante lo spostamento")
	}
	target, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	complete := false
	defer func() {
		target.Close()
		if !complete {
			os.Remove(destination)
		}
	}()
	if err := copyRegularFile(source, target); err != nil {
		return "", err
	}
	if err := target.Chmod(info.Mode().Perm()); err != nil {
		return "", err
	}
	if err := os.Chtimes(destination, info.ModTime(), info.ModTime()); err != nil {
		return "", err
	}
	if err := target.Sync(); err != nil {
		return "", err
	}
	if err := target.Close(); err != nil {
		return "", err
	}
	if err := files.Remove(relative); err != nil {
		return "", err
	}
	complete = true
	return destination, nil
}

func restoreTrash(root, originalRelative, trashPath string) error {
	original, err := cleanRelative(originalRelative)
	if err != nil {
		return err
	}
	files, _, err := openRoot(root)
	if err != nil {
		return err
	}
	defer files.Close()
	if _, err := regularFileInfo(files, original); err == nil {
		return fmt.Errorf("l'originale esiste gia': non sovrascrivo %s", original)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	destination := filepath.Join(files.Name(), filepath.FromSlash(original))
	return movePathToExclusive(trashPath, destination)
}

func movePathToExclusive(sourcePath, destinationPath string) error {
	info, err := os.Lstat(sourcePath)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("la sorgente dello storico non e' un file regolare")
	}
	if err := os.Link(sourcePath, destinationPath); err == nil {
		if removeErr := os.Remove(sourcePath); removeErr != nil {
			return fmt.Errorf("originale ripristinato ma storico non rimosso: %w", removeErr)
		}
		return nil
	} else if errors.Is(err, fs.ErrExist) {
		return err
	}
	return copyPathToExclusive(sourcePath, destinationPath, true)
}

// promoteVersion moves source to destination without ever overwriting an
// existing destination. A hard link provides an atomic no-clobber claim on the
// usual local filesystems; an exclusive copy is the fallback for filesystems
// that do not support hard links.
func promoteVersion(files *os.Root, source, destination string, sourceInfo fs.FileInfo) error {
	err := files.Link(source, destination)
	if err == nil {
		linkedInfo, statErr := files.Lstat(destination)
		if statErr != nil || !linkedInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, linkedInfo) {
			if statErr == nil {
				files.Remove(destination)
			}
			return fmt.Errorf("la copia promossa non e' un file regolare")
		}
		return finishPromotion(files, source, destination)
	}
	if errors.Is(err, fs.ErrExist) {
		destinationInfo, statErr := files.Lstat(destination)
		if statErr == nil && destinationInfo.Mode().IsRegular() && os.SameFile(sourceInfo, destinationInfo) {
			return finishPromotion(files, source, destination)
		}
		return err
	}

	if err := copyRootToExclusive(files, source, destination, sourceInfo); err != nil {
		return err
	}
	return finishPromotion(files, source, destination)
}

func finishPromotion(files *os.Root, source, destination string) error {
	if err := files.Remove(source); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%w: impossibile rimuovere %s dopo aver creato %s: %v", errPromotionCompleted, source, destination, err)
	}
	return nil
}

func copyRootToExclusive(files *os.Root, source, destination string, sourceInfo fs.FileInfo) error {
	target, err := files.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		target.Close()
		if !committed {
			if info, err := target.Stat(); err == nil {
				if current, err := files.Lstat(destination); err == nil && os.SameFile(info, current) {
					files.Remove(destination)
				}
			}
		}
	}()
	sourceFile, err := files.Open(source)
	if err != nil {
		return err
	}
	defer sourceFile.Close()
	openedInfo, err := sourceFile.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(sourceInfo, openedInfo) {
		return fmt.Errorf("la copia sorgente e' cambiata durante la promozione")
	}
	if err := copyRegularFile(sourceFile, target); err != nil {
		return err
	}
	if err := target.Chmod(sourceInfo.Mode().Perm()); err != nil {
		return err
	}
	if err := files.Chtimes(destination, sourceInfo.ModTime(), sourceInfo.ModTime()); err != nil {
		return err
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	committed = true
	return nil
}

func copyPathToExclusive(sourcePath, destinationPath string, removeSource bool) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("la sorgente dello storico non e' un file regolare")
	}
	target, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		target.Close()
		if !committed {
			os.Remove(destinationPath)
		}
	}()
	if err := copyRegularFile(source, target); err != nil {
		return err
	}
	if err := target.Chmod(info.Mode().Perm()); err != nil {
		return err
	}
	if err := os.Chtimes(destinationPath, info.ModTime(), info.ModTime()); err != nil {
		return err
	}
	if err := target.Sync(); err != nil {
		return err
	}
	if err := target.Close(); err != nil {
		return err
	}
	committed = true
	if removeSource {
		return os.Remove(sourcePath)
	}
	return nil
}
