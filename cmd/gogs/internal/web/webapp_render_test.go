package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"gogs.io/gogs/internal/context"
)

func TestRenderIndex_injection(t *testing.T) {
	customDir := t.TempDir()
	injectDir := filepath.Join(customDir, "templates", "inject")
	require.NoError(t, os.MkdirAll(injectDir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(injectDir, "head.tmpl"), []byte(`<meta name="head-marker">`), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(injectDir, "footer.tmpl"), []byte(`<script>footerMarker()</script>`), 0600))
	t.Setenv("GOGS_CUSTOM", customDir)

	inject, err := readInjectContent()
	require.NoError(t, err)

	shell := `<html><head>{{.WebContext}}</head><body><div id="root"></div></body></html>`
	got, err := renderIndex([]byte(shell), context.WebContext{Lang: "en-US"}, inject)
	require.NoError(t, err)

	out := string(got)
	assert.Contains(t, out, `<meta name="head-marker">`)
	assert.Contains(t, out, `<script>footerMarker()</script></body>`)
	// The head injection lands inside <head>, before </head>.
	assert.Less(t, strings.Index(out, `<meta name="head-marker">`), strings.Index(out, `</head>`))
}

// TestRenderIndex_absoluteAssets checks that asset references are rewritten to
// absolute paths even at the root mount. Vite emits relative paths, which a
// browser resolves against the current directory, so the shell served at
// "/user/sign-in" would request "/user/assets/..." and never load the app.
func TestRenderIndex_absoluteAssets(t *testing.T) {
	t.Setenv("GOGS_CUSTOM", t.TempDir())

	inject, err := readInjectContent()
	require.NoError(t, err)

	shell := `<html><head>{{.WebContext}}` +
		`<script type="module" src="./assets/index.js"></script>` +
		`<link rel="stylesheet" href="./assets/index.css">` +
		`<link rel="shortcut icon" href="/img/favicon.png">` +
		`</head><body></body></html>`

	t.Run("root mount", func(t *testing.T) {
		got, err := renderIndex([]byte(shell), context.WebContext{Lang: "en-US"}, inject)
		require.NoError(t, err)

		out := string(got)
		assert.NotContains(t, out, `./assets/`, "relative asset paths break pages below the root")
		assert.Contains(t, out, `src="/assets/index.js"`)
		assert.Contains(t, out, `href="/assets/index.css"`)
	})

	t.Run("subpath mount", func(t *testing.T) {
		got, err := renderIndex([]byte(shell), context.WebContext{Lang: "en-US", SubURL: "/gogs"}, inject)
		require.NoError(t, err)

		out := string(got)
		assert.NotContains(t, out, `./assets/`)
		assert.Contains(t, out, `src="/gogs/assets/index.js"`)
		assert.Contains(t, out, `href="/gogs/assets/index.css"`)
	})
}
