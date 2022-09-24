package convert

import (
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestGetApkBuildFile(t *testing.T) {
	configFilename := "/aports/tree/main/util-macros/APKBUILD"

	data, err := os.ReadFile(filepath.Join("testdata", "APKBUILD_DATA"))
	assert.NoError(t, err)
	assert.NotEmpty(t, data)

	// Start a local HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
		// Test request parameters
		assert.Equal(t, req.URL.String(), configFilename)
		// Send response to be tested
		_, err = rw.Write(data)
		assert.NoError(t, err)
	}))

	// Close the server when test finishes
	defer server.Close()

	context, err := New(server.URL + configFilename)
	assert.NoError(t, err)

	context.Client = server.Client()
	err = context.getApkBuildFile()
	assert.NoError(t, err)

	assert.Equal(t, "util-macros", context.ApkBuild.PackageName)
	assert.Equal(t, "1.19.3", context.ApkBuild.PackageVerion)
	assert.Equal(t, "0", context.ApkBuild.PackageRel)
	assert.Equal(t, "X.Org Autotools macros", context.ApkBuild.PackageDesc)
	assert.Equal(t, "https://xorg.freedesktop.org", context.ApkBuild.PackageUrl)
	assert.Equal(t, "noarch", context.ApkBuild.Arch)
	assert.Equal(t, "MIT", context.ApkBuild.License)
	assert.Equal(t, "https://www.x.org/releases/individual/util/util-macros-$pkgver.tar.bz2", context.ApkBuild.Source)

}
