package update

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// This is a deliberately strict semver parser. The updater makes a security
// decision from the version, so loose atoi-based comparisons (which silently
// turn "9.x" into 9) are not acceptable.
var semverRE = regexp.MustCompile(`^v?(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`)

type semver struct {
	major, minor, patch int64
	pre                 string
}

func parseSemver(value string) (semver, error) {
	match := semverRE.FindStringSubmatch(strings.TrimSpace(value))
	if match == nil {
		return semver{}, fmt.Errorf("versione non valida (atteso semver): %q", value)
	}
	numbers := make([]int64, 3)
	for index, part := range match[1:4] {
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return semver{}, fmt.Errorf("versione non valida: %q", value)
		}
		numbers[index] = n
	}
	if pre := match[4]; pre != "" {
		for _, id := range strings.Split(pre, ".") {
			if id == "" {
				return semver{}, fmt.Errorf("versione non valida: %q", value)
			}
			if allDigits(id) && len(id) > 1 && strings.HasPrefix(id, "0") {
				return semver{}, fmt.Errorf("versione non valida: %q", value)
			}
		}
	}
	return semver{major: numbers[0], minor: numbers[1], patch: numbers[2], pre: match[4]}, nil
}

func allDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return value != ""
}

func compareSemver(left, right semver) int {
	for _, pair := range [][2]int64{{left.major, right.major}, {left.minor, right.minor}, {left.patch, right.patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	if left.pre == right.pre {
		return 0
	}
	if left.pre == "" {
		return 1 // a release is newer than any of its prereleases
	}
	if right.pre == "" {
		return -1
	}
	leftIDs, rightIDs := strings.Split(left.pre, "."), strings.Split(right.pre, ".")
	for index := 0; index < len(leftIDs) && index < len(rightIDs); index++ {
		a, b := leftIDs[index], rightIDs[index]
		if a == b {
			continue
		}
		aNum, bNum := allDigits(a), allDigits(b)
		switch {
		case aNum && bNum:
			aValue, _ := strconv.ParseInt(a, 10, 64)
			bValue, _ := strconv.ParseInt(b, 10, 64)
			if aValue < bValue {
				return -1
			}
			return 1
		case aNum:
			return -1 // numeric identifiers have lower precedence
		case bNum:
			return 1
		default:
			if a < b {
				return -1
			}
			return 1
		}
	}
	if len(leftIDs) < len(rightIDs) {
		return -1
	}
	return 1
}

func semverLess(left, right semver) bool { return compareSemver(left, right) < 0 }
