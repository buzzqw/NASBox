package serverd

import (
	"fmt"
	"os"
	"regexp"
	"strconv"
	"time"
)

var revisionPattern = regexp.MustCompile(`^[0-9]{1,18}$`)

func printChangeWait(result, revision string) {
	fmt.Println("NASBOX_CHANGE_WAIT_V1")
	fmt.Println("RESULT=" + result)
	fmt.Println("REVISION=" + revision)
}

// cmdWaitForRevision implements the change feed: the client holds one SSH
// session open and waits until manifest.revision is greater than the revision
// it already knows. Polling every second is enough on this small NAS and
// keeps the daemon dependency-free (no inotifywait needed).
func cmdWaitForRevision(ctx *Context, args []string) error {
	if len(args) != 2 || !revisionPattern.MatchString(args[0]) || !revisionPattern.MatchString(args[1]) {
		fmt.Fprintln(os.Stderr, "wait revision: argomenti non validi")
		printChangeWait("ERROR", "0")
		return exitCode(2)
	}
	requested, _ := strconv.ParseInt(args[0], 10, 64)
	timeout, _ := strconv.ParseInt(args[1], 10, 64)
	if timeout > 3600 {
		fmt.Fprintln(os.Stderr, "wait revision: timeout fuori limite")
		printChangeWait("ERROR", "0")
		return exitCode(2)
	}
	read := func() (int64, bool) {
		raw, err := os.ReadFile(ctx.paths().Revision)
		if err != nil {
			return 0, false
		}
		text := string(raw)
		for len(text) > 0 && (text[len(text)-1] == '\n' || text[len(text)-1] == '\r') {
			text = text[:len(text)-1]
		}
		if !revisionPattern.MatchString(text) {
			return 0, false
		}
		value, err := strconv.ParseInt(text, 10, 64)
		return value, err == nil
	}
	current, ok := read()
	if !ok {
		fmt.Fprintln(os.Stderr, "wait revision: manifest.revision non valido")
		printChangeWait("ERROR", "0")
		return exitCode(1)
	}
	if current > requested || timeout == 0 {
		result := "TIMEOUT"
		if current > requested {
			result = "CHANGED"
		}
		printChangeWait(result, strconv.FormatInt(current, 10))
		return nil
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for time.Now().Before(deadline) {
		time.Sleep(time.Second)
		current, ok = read()
		if ok && current > requested {
			printChangeWait("CHANGED", strconv.FormatInt(current, 10))
			return nil
		}
	}
	printChangeWait("TIMEOUT", strconv.FormatInt(current, 10))
	return nil
}
