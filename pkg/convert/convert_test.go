package convert

import (
	"chainguard.dev/melange/pkg/build"
	"github.com/stretchr/testify/assert"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

func TestContext_getSourceSha(t *testing.T) {

	type fields struct {
		ExpectedSha    string
		TestUrl        string
		PackageVersion string
	}
	tests := []struct {
		name    string
		fields  fields
		wantErr assert.ErrorAssertionFunc
	}{
		{
			name: "foo-1.2.3.tar.xz",
			fields: fields{
				TestUrl:        "/releases/foo/foo-$pkgver.tar.xz",
				PackageVersion: "1.2.3",
				ExpectedSha:    "6b23c4b39242db1d58ab397387b7a3a325e903cd4df332f5a089ac63cc1ca049",
			},
			wantErr: nil,
		},
	}

	for _, tt := range tests {
		data, err := os.ReadFile(filepath.Join("testdata", tt.name))
		assert.NoError(t, err)
		source := strings.ReplaceAll(tt.fields.TestUrl, "$pkgver", tt.fields.PackageVersion)
		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			// Test request parameters
			assert.Equal(t, req.URL.String(), source)
			// Send response to be tested
			_, err = rw.Write(data)
			assert.NoError(t, err)
		}))

		t.Run(tt.name, func(t *testing.T) {

			c := context{
				ApkBuild: &ApkBuild{
					Source:         server.URL + tt.fields.TestUrl,
					PackageVersion: tt.fields.PackageVersion,
				},
				Client:                server.Client(),
				GeneratedMelageConfig: &GeneratedMelageConfig{},
			}

			with := map[string]string{
				"uri":             server.URL + source,
				"expected-sha256": tt.fields.ExpectedSha,
			}
			pipeline := build.Pipeline{Uses: "fetch", With: with}
			err = c.buildFetchStep()
			assert.NoError(t, err)
			assert.Equalf(t, pipeline, c.GeneratedMelageConfig.Pipeline[0], "expected sha incorrect")

		})
	}
}
