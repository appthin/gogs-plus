package web

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAppShellSegments_coverSPARoutes guards the explicit "/user" routes against
// drifting from the React router. A page defined only in the frontend is never
// reached, because "/:username/:reponame" matches it first and rejects the
// path as an unknown repository.
func TestAppShellSegments_coverSPARoutes(t *testing.T) {
	src, err := os.ReadFile("../../../../web/src/routes/user.tsx")
	require.NoError(t, err)

	paths := regexp.MustCompile(`path:\s*"(/user/[^"]+)"`).FindAllStringSubmatch(string(src), -1)
	require.NotEmpty(t, paths, "no route paths found in web/src/routes/user.tsx")

	for _, match := range paths {
		segment := match[1][len("/user"):]
		assert.True(t, slices.Contains(appShellSegments, segment),
			"%s is defined in web/src/routes/user.tsx but missing from appShellSegments", match[1])
	}
}
