package app

import (
	"strconv"
	"strings"
)

// Previews (feature 049). A preview environment is an environment named
// "pr-<n>", where n is a pull request number. The name is the only thing that
// says so: no column, no flag. The prefix "pr-" is reserved for them, so an
// environment that starts with it and isn't a preview name is refused.

// previewPrefix starts the name of every preview environment.
const previewPrefix = "pr-"

// maxPreviewDigits is the most digits a pull request number has (999999999).
const maxPreviewDigits = 9

// PreviewNumber reads the pull request number from a preview environment name:
// "pr-12" is 12. ok is false for any other name, including "pr-0", "pr-012"
// (no leading zero), "pr-" and a number of more than nine digits, which keeps
// the name unambiguous: one pull request, one name.
func PreviewNumber(name string) (n int, ok bool) {
	digits, found := strings.CutPrefix(name, previewPrefix)
	if !found || digits == "" || len(digits) > maxPreviewDigits || digits[0] == '0' {
		return 0, false
	}
	for _, c := range digits {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

// PreviewName is the environment name of pull request n.
func PreviewName(n int) string { return previewPrefix + strconv.Itoa(n) }

// IsPreviewEnvironment says whether name is a preview environment name.
func IsPreviewEnvironment(name string) bool {
	_, ok := PreviewNumber(name)
	return ok
}

// ValidEnvironmentName checks the name of an environment someone creates: a
// name (ValidName) that doesn't start with the reserved "pr-" unless it is a
// preview name. The error is a *FieldError on field.
func ValidEnvironmentName(field, name string) error {
	if err := ValidName(field, name); err != nil {
		return err
	}
	if strings.HasPrefix(name, previewPrefix) && !IsPreviewEnvironment(name) {
		return NewFieldError(field, "names starting with %s are kept for previews: pr-<pull request number>, such as pr-12", previewPrefix)
	}
	return nil
}
