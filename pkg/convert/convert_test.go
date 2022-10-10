package convert

import (
	"bytes"
	"chainguard.dev/melange/pkg/build"
	"github.com/stretchr/testify/assert"
	"golang.org/x/time/rate"
	"gopkg.in/yaml.v3"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestGetApkDependencies(t *testing.T) {

	deps, err := os.ReadDir(filepath.Join("testdata", "deps"))
	assert.NoError(t, err)
	assert.NotEmpty(t, deps)

	var filenames []string
	for _, dep := range deps {
		filenames = append(filenames, "/"+dep.Name())
	}

	// Start a local HTTP server
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {

		// assert requests dependency is in the list of test files
		assert.True(t, contains(filenames, req.URL.String()), "requests file does not match any test files")

		// send response to be tested
		data, err := os.ReadFile(filepath.Join("testdata", "deps", "/"+req.URL.String()))
		assert.NoError(t, err)
		assert.NotEmpty(t, data)
		_, err = rw.Write(data)
		assert.NoError(t, err)
	}))

	// Close the server when test finishes
	defer server.Close()

	context := getTestContext(t, server)

	// the top level APKBUILD is cheese
	err = context.Generate(server.URL + "/" + "cheese")
	assert.NoError(t, err)

	// assert all dependencies were found
	_, exists := context.ApkConvertors[server.URL+"/bar"]
	assert.True(t, exists, "/bar not found")
	_, exists = context.ApkConvertors[server.URL+"/beer"]
	assert.True(t, exists, "/beer not found")
	_, exists = context.ApkConvertors[server.URL+"/cheese"]
	assert.True(t, exists, "/cheese not found")
	_, exists = context.ApkConvertors[server.URL+"/crisps"]
	assert.True(t, exists, "/crisps not found")
	_, exists = context.ApkConvertors[server.URL+"/foo"]
	assert.True(t, exists, "/foo not found")
	_, exists = context.ApkConvertors[server.URL+"/wine"]
	assert.True(t, exists, "/wine not found")

}

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

	context := getTestContext(t, server)

	context.Client.client = server.Client()
	err = context.getApkBuildFile(server.URL + configFilename)
	assert.NoError(t, err)

	assert.Equal(t, 1, len(context.ApkConvertors), "apk converter not found")

	apkbuild := context.ApkConvertors[server.URL+configFilename].ApkBuild
	assert.Equal(t, "libx11", apkbuild.PackageName)
	assert.Equal(t, "1.8.1", apkbuild.PackageVersion)
	assert.Equal(t, "1", apkbuild.PackageRel)
	assert.Equal(t, "X11 client-side library", apkbuild.PackageDesc)
	assert.Equal(t, "https://xorg.freedesktop.org/", apkbuild.PackageUrl)
	assert.Equal(t, []string{"all"}, apkbuild.Arch)
	assert.Equal(t, "custom:XFREE86", apkbuild.License)
	assert.Equal(t, []string{"https://www.x.org/releases/individual/lib/libX11-$pkgver.tar.xz"}, apkbuild.Source)
	assert.Equal(t, []string{"$pkgname-static", "$pkgname-dev", "$pkgname-doc"}, apkbuild.SubPackages)
	assert.Equal(t, []string{"libxcb-dev", "xtrans"}, apkbuild.DependDev)
	assert.Equal(t, []string{"$depends_dev", "xorgproto", "util-macros", "xmlto"}, apkbuild.MakeDepends)

}

func TestContext_getSourceSha(t *testing.T) {

	type fields struct {
		ExpectedSha    string
		TestUrl        string
		PackageVersion string
	}
	var tests = []struct {
		name   string
		fields fields
	}{
		{
			name: "tar.xz",
			fields: fields{
				TestUrl:        "foo-$pkgver.tar.xz",
				PackageVersion: "1.2.3",
				ExpectedSha:    "6b23c4b39242db1d58ab397387b7a3a325e903cd4df332f5a089ac63cc1ca049",
			},
		},
		{
			name: "tar.gz",
			fields: fields{
				TestUrl:        "bar-$pkgver.tar.gz",
				PackageVersion: "4.5.6",
				ExpectedSha:    "cc2c52929ace57623ff517408a577e783e10042655963b2c8f0633e109337d7a",
			},
		},
		{
			name: "tar.bz2",
			fields: fields{
				TestUrl:        "cheese-$pkgver.tar.bz2",
				PackageVersion: "7.8.9",
				ExpectedSha:    "8452aa9c8cefc805c8930bc53394c7de15f43edc82dd86e619d794cd7f60b410",
			},
		},
	}
	for _, tt := range tests {
		// read testdata file
		testFilename := strings.ReplaceAll(tt.fields.TestUrl, "$pkgver", tt.fields.PackageVersion)
		data, err := os.ReadFile(filepath.Join("testdata", testFilename))
		assert.NoError(t, err)

		server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, req *http.Request) {
			// Test request parameters
			assert.Equal(t, req.URL.String(), "/"+testFilename)

			// Send response to be tested
			_, err = rw.Write(data)
			assert.NoError(t, err)

		}))

		// initialise Context with test values
		c := getTestContext(t, server)

		c.ApkConvertors[tt.name] = ApkConvertor{
			ApkBuild: &ApkBuild{
				Source:         []string{server.URL + "/" + tt.fields.TestUrl},
				PackageVersion: tt.fields.PackageVersion,
			},
			GeneratedMelageConfig: &GeneratedMelageConfig{},
		}

		t.Run(tt.name, func(t *testing.T) {

			with := map[string]string{
				"uri":             server.URL + "/" + strings.ReplaceAll(testFilename, tt.fields.PackageVersion, "${{package.version}}"),
				"expected-sha256": tt.fields.ExpectedSha,
			}
			pipeline := build.Pipeline{Uses: "fetch", With: with}

			assert.NoError(t, c.buildFetchStep(c.ApkConvertors[tt.name]))
			assert.Equalf(t, pipeline, c.ApkConvertors[tt.name].GeneratedMelageConfig.Pipeline[0], "expected sha incorrect")

		})
	}
}
func Test_context_mapMelange(t *testing.T) {

	apkBuild := &ApkBuild{
		PackageName:    "test_pkg",
		PackageVersion: "1.2.3",
		PackageRel:     "1",
		PackageDesc:    "test package description",
		PackageUrl:     "https://foo.com",
		Arch:           []string{"all"},
		License:        "MIT",
		BuilderType:    "make",
	}

	tests := []struct {
		name        string
		subPackages []string
		apkBuild    *ApkBuild
	}{
		{
			name: "no_sub_packages",
		},
		{
			name:        "with_unrecognised_sub_packages",
			subPackages: []string{"foo"},
		},
		{
			name:        "with_multi_sub_packages",
			subPackages: []string{"test_pkg-doc", "test_pkg-dev"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			apkBuild.SubPackages = tt.subPackages
			c := Context{
				NavigationMap: &NavigationMap{
					ApkConvertors: make(map[string]ApkConvertor),
				},
			}
			c.ApkConvertors[tt.name] = ApkConvertor{
				ApkBuild:              apkBuild,
				GeneratedMelageConfig: &GeneratedMelageConfig{},
			}
			c.ApkConvertors[tt.name].mapMelange()

			expected, err := os.ReadFile(filepath.Join("testdata", tt.name+".yaml"))
			assert.NoError(t, err)

			config := c.ApkConvertors[tt.name].GeneratedMelageConfig
			actual, err := yaml.Marshal(&config)

			assert.NoError(t, err)

			assert.YAMLEqf(t, string(expected), string(actual), "generated melange yaml not the same as expected")
		})
	}
}
func TestScannerError(t *testing.T) {

	data, err := os.ReadFile(filepath.Join("testdata", "scanner_error.yaml"))
	assert.NoError(t, err)
	c := Context{
		NavigationMap: &NavigationMap{
			ApkConvertors: make(map[string]ApkConvertor),
		},
		Logger: log.New(log.Writer(), "unittest: ", log.LstdFlags|log.Lmsgprefix),
	}
	key := "https://git.alpinelinux.org/aports/plain/main/libxext/APKBUILD"
	c.ApkConvertors[key] = ApkConvertor{
		ApkBuild: &ApkBuild{},
	}

	c.parseApkBuild(bytes.NewReader(data), key)
	assert.NoError(t, err)
	assert.Equal(t, []string{"https://www.x.org/releases/individual/lib/libXext-$pkgver.tar.bz2"}, c.ApkConvertors[key].ApkBuild.Source)
}

func TestMultilineParsing(t *testing.T) {

	data, err := os.ReadFile(filepath.Join("testdata", "multi_source", "APKBUILD-short"))
	assert.NoError(t, err)
	c := Context{
		NavigationMap: &NavigationMap{
			ApkConvertors: make(map[string]ApkConvertor),
		},
		Logger: log.New(log.Writer(), "unittest: ", log.LstdFlags|log.Lmsgprefix),
	}
	key := "https://git.alpinelinux.org/aports/plain/main/icu/APKBUILD"
	c.ApkConvertors[key] = ApkConvertor{
		ApkBuild: &ApkBuild{},
	}

	c.parseApkBuild(bytes.NewReader(data), key)
	assert.NoError(t, err)
	assert.Equal(t, 7, len(c.ApkConvertors[key].ApkBuild.Source))

	assert.Equal(t, "1fd2a20aef48369d1f06e2bb74584877b8ad0eb529320b976264ec2db87420bae242715795f372dbc513ea80047bc49077a064e78205cd5e8b33d746fd2a2912", c.ApkConvertors[key].ApkBuild.Sha512sums["icu4c-71_1-src.tgz"])
	assert.Equal(t, "05eb134a963a541a280e49e4d0aca07e480fef14daa0108c8fb9add18c150c9d34c8cbc46386c07909d511f7777eb3ea9f494001f191b84a7de0be8047da8b56", c.ApkConvertors[key].ApkBuild.Sha512sums["icu4c-71_1-data.zip"])
	assert.Equal(t, "b031e520d41cc313012a0a9d9c4eed51aee9e04213b810bcec32e18d0964f4f26448b989879a9d8901d29024da08ce2ac89c8c6d321c85d78f6414b5edebc1a4", c.ApkConvertors[key].ApkBuild.Sha512sums["001-fix-heap-buffer-overflow.patch"])
	assert.Equal(t, "de2cd008406d133cc838388f5a109560d29323e0a4c8c6306f712a536b6d90846d44bc5f691514621653f33a2929c0d84fa9c54d61d5ddf4606243df63c7e139", c.ApkConvertors[key].ApkBuild.Sha512sums["skip-flawed-tests.patch"])
}

func getTestContext(t *testing.T, server *httptest.Server) Context {
	return Context{
		NavigationMap: &NavigationMap{
			ApkConvertors: make(map[string]ApkConvertor),
		},

		Client: &RLHTTPClient{
			client: server.Client(),

			// for unit tests we don't need to rate limit requests
			Ratelimiter: rate.NewLimiter(rate.Every(1*time.Second), 20), // 10 request every 10 seconds
		},
		Logger: log.New(log.Writer(), "test: ", log.LstdFlags|log.Lmsgprefix),
		OutDir: t.TempDir(),
	}
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}
