package serverd

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var serverBinaryPattern = regexp.MustCompile(`^nasbox-server-(\d+\.\d+\.\d+)$`)

func compareVersion(left, right string) int {
	parse := func(value string) []int {
		parts := []int{}
		for _, field := range strings.Split(value, ".") {
			n, _ := strconv.Atoi(field)
			parts = append(parts, n)
		}
		return parts
	}
	a, b := parse(left), parse(right)
	for i := 0; i < len(a) || i < len(b); i++ {
		x, y := 0, 0
		if i < len(a) {
			x = a[i]
		}
		if i < len(b) {
			y = b[i]
		}
		if x < y {
			return -1
		}
		if x > y {
			return 1
		}
	}
	return 0
}

func cmdServerUpdateCheck(ctx *Context) error {
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Dir(binary))
	if err != nil {
		return err
	}
	best := ""
	for _, entry := range entries {
		match := serverBinaryPattern.FindStringSubmatch(entry.Name())
		if match == nil || compareVersion(match[1], version) <= 0 {
			continue
		}
		if best == "" || compareVersion(best, match[1]) < 0 {
			best = match[1]
		}
	}
	if best == "" {
		fmt.Println("Nessun aggiornamento server disponibile.")
		return nil
	}
	fmt.Printf("Aggiornamento server disponibile: %s\n", best)
	return nil
}

func cmdServerUpdateInstall(ctx *Context, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("uso: --server-update-install <versione>")
	}
	wanted := args[0]
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	if !serverBinaryPattern.MatchString("nasbox-server-" + wanted) {
		return fmt.Errorf("versione server non valida: %s", wanted)
	}
	source := filepath.Join(filepath.Dir(binary), "nasbox-server-"+wanted)
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("binario server versionato non trovato: %s", source)
	}
	if sidecar, err := os.ReadFile(source + ".sha256"); err == nil {
		digest, err := sha256File(source)
		if err != nil || digest != strings.TrimSpace(string(sidecar)) {
			return fmt.Errorf("sha256 del binario server non corrisponde")
		}
	}
	if err := copyFile(binary+".prev", binary, 0o600); err != nil {
		return err
	}
	if err := copyFile(binary+".new", source, 0o755); err != nil {
		return err
	}
	if err := os.Rename(binary+".new", binary); err != nil {
		return err
	}
	if err := os.WriteFile(binary+".version", []byte(wanted+"\n"), 0o644); err != nil {
		return err
	}
	if err := cmdRestart(ctx); err != nil {
		return err
	}
	fmt.Printf("server aggiornato a %s\n", wanted)
	return nil
}

func copyFile(target, source string, mode os.FileMode) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
