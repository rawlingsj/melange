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

	assert.Equal(t, "libx11", context.ApkBuild.PackageName)
	assert.Equal(t, "1.8.1", context.ApkBuild.PackageVersion)
	assert.Equal(t, "1", context.ApkBuild.PackageRel)
	assert.Equal(t, "X11 client-side library", context.ApkBuild.PackageDesc)
	assert.Equal(t, "https://xorg.freedesktop.org/", context.ApkBuild.PackageUrl)
	assert.Equal(t, "all", context.ApkBuild.Arch)
	assert.Equal(t, "custom:XFREE86", context.ApkBuild.License)
	assert.Equal(t, "https://www.x.org/releases/individual/lib/libX11-$pkgver.tar.xz", context.ApkBuild.Source)
	assert.Equal(t, []string{"$pkgname-static", "$pkgname-dev", "$pkgname-doc"}, context.ApkBuild.SubPackages)
	assert.Equal(t, []string{"libxcb-dev", "xtrans"}, context.ApkBuild.DependDev)
	assert.Equal(t, []string{"$depends_dev", "xorgproto", "util-macros", "xmlto"}, context.ApkBuild.MakeDepends)

}
